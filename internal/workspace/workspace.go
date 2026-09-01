package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func ModuleRoot(cwd string) (string, error) {
	cmd := exec.Command("go", "env", "GOMOD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD failed: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull || gomod == "/dev/null" {
		return "", fmt.Errorf("no Go module found from %s", cwd)
	}
	return filepath.Dir(gomod), nil
}

type Package struct {
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	ImportPath string
	Deps       []string
	ForTest    string
	Error      *struct{ Err string }
}

func ListPackages(cwd string, patterns []string) ([]Package, error) {
	args := append([]string{"list", "-json", "-e"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list failed: %w", err)
	}
	return decodePackages(out)
}

// TestScopes computes, for every package within patterns that has its
// own tests, the minimal set of test package import paths needed to
// safely validate a mutant in that package: itself, plus every other
// package (within the same patterns) whose test binary transitively
// depends on it. This is purely additive data used only to narrow an
// individual mutant's own `go test` invocation when a person opts into
// it (config.NarrowTestScope) -- it never changes which mutants are
// discovered or which files are mutation candidates, and does not touch
// ListPackages/SourceFiles above, which every other part of this tool
// still uses unmodified.
//
// The transitive closure is asked of the go tool itself (`go list -deps
// -test`) rather than computed by walking Imports by hand, specifically
// because a naive walk of only "Imports" would miss a real and not even
// unusual case: a package's dependency reachable only through an
// external "foo_test" test file (e.g. an integration-style test),
// which never appears in the package's own Imports at all. `go list`'s
// ForTest field marks exactly the synthetic packages representing a
// compiled test binary, and that package's own Deps field is the
// correct, already-computed transitive closure including such
// test-only edges -- confirmed against a synthetic fixture with an
// external test package before this was written the way it is.
func TestScopes(cwd string, patterns []string) (map[string][]string, error) {
	args := append([]string{"list", "-json", "-deps", "-test", "-e"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps -test failed: %w", err)
	}
	pkgs, err := decodePackages(out)
	if err != nil {
		return nil, err
	}

	// depsByTarget[T] is the set of packages (including T itself) that a
	// mutant in T needs tested -- built by unioning every test-binary
	// variant's Deps for that target, since a package can have both an
	// internal ("package foo") and external ("package foo_test") test
	// file at once, each contributing its own, independently-computed
	// Deps set that must be combined to get the whole picture.
	depsByTarget := map[string]map[string]bool{}
	for _, p := range pkgs {
		if p.ForTest == "" {
			continue
		}
		set := depsByTarget[p.ForTest]
		if set == nil {
			set = map[string]bool{p.ForTest: true}
			depsByTarget[p.ForTest] = set
		}
		for _, d := range p.Deps {
			if strings.Contains(d, " [") {
				continue // a bracket-suffixed test-variant reference, never a real mutated package's plain import path
			}
			set[d] = true
		}
	}

	// Invert: for each package a mutant could land in, which test
	// targets' scopes include it.
	index := map[string]map[string]bool{}
	for target, deps := range depsByTarget {
		for d := range deps {
			if index[d] == nil {
				index[d] = map[string]bool{}
			}
			index[d][target] = true
		}
	}

	out2 := make(map[string][]string, len(index))
	for pkg, testPkgs := range index {
		list := make([]string, 0, len(testPkgs))
		for t := range testPkgs {
			list = append(list, t)
		}
		sort.Strings(list)
		out2[pkg] = list
	}
	return out2, nil
}

func SourceFiles(root string, pkgs []Package) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, p := range pkgs {
		if p.Error != nil && p.Error.Err != "" {
			return nil, fmt.Errorf("package %s: %s", p.ImportPath, p.Error.Err)
		}
		for _, name := range append(append([]string{}, p.GoFiles...), p.CgoFiles...) {
			abs := filepath.Join(p.Dir, name)
			rel, err := filepath.Rel(root, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("source file %s is outside module root", abs)
			}
			rel = filepath.ToSlash(rel)
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// sandboxEntries walks root exactly as CopyModule copies it -- the same
// order, and skipping exactly the same directories (.git,
// .mutation-judge, and the resolved cache directory) -- invoking fn
// once for every directory and non-directory entry CopyModule would
// otherwise place into the sandbox. This is the single shared source
// of truth for "what will a test run inside the sandbox actually see":
// Digest and CopyModule independently walking the tree with their own,
// separately maintained skip/include rules is exactly how they drifted
// apart before (see ISSUES.md, "Digest does not hash what tests can
// observe") -- Digest hashed only *.go/go.mod/go.sum/go.work/go.work.sum
// while CopyModule copied everything else alongside it too (//go:embed
// payloads, cgo .c/.h/.s, testdata/, go.env, and any other file a test
// can read by path), so changing any of those produced a cache hit with
// stale results. Sharing this walk instead of two separately maintained
// file-selection functions makes that class of drift structurally
// impossible to reintroduce, not just fixed once.
func sandboxEntries(root, cacheDir string, fn func(path, rel string, d fs.DirEntry) error) error {
	cacheAbs := cacheDir
	if !filepath.IsAbs(cacheAbs) {
		cacheAbs = filepath.Join(root, cacheDir)
	}
	cacheAbs, err := filepath.Abs(cacheAbs)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".mutation-judge" || filepath.Clean(path) == cacheAbs) {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return fn(path, filepath.ToSlash(rel), d)
	})
}

func CopyModule(root, cacheDir string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "mutation-judge-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	err = sandboxEntries(root, cacheDir, func(path, rel string, d fs.DirEntry) error {
		dst := filepath.Join(tmp, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, dst, info.Mode())
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp, cleanup, nil
}

func Apply(root string, rel string, start, end int, replacement string) (func() error, error) {
	path, err := secureExistingPath(root, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to mutate symlinked source file %s", rel)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if start < 0 || end <= start || end > len(original) {
		return nil, fmt.Errorf("invalid mutation span %d:%d for %s (%d bytes)", start, end, rel, len(original))
	}
	mutated := make([]byte, 0, len(original)-(end-start)+len(replacement))
	mutated = append(mutated, original[:start]...)
	mutated = append(mutated, replacement...)
	mutated = append(mutated, original[end:]...)
	if err := writeFileAtomic(path, mutated, info.Mode().Perm()); err != nil {
		return nil, err
	}
	return func() error { return writeFileAtomic(path, original, info.Mode().Perm()) }, nil
}

func secureExistingPath(root, rel string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	inside, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("mutation path escapes sandbox root: %s", rel)
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return "", err
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("mutation path resolves outside sandbox root: %s", rel)
	}
	return pathAbs, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	return writeFileAtomicWithRename(path, data, mode, os.Rename)
}

func writeFileAtomicWithRename(path string, data []byte, mode os.FileMode, rename func(string, string) error) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mutation-judge-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	defer cleanup()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return rename(tmpName, path)
}

// Digest fingerprints every input that CopyModule places into the
// sandbox and a test run can therefore observe -- not just *.go files,
// but //go:embed payloads, cgo .c/.h/.s sources, testdata/ fixtures,
// go.env, and anything else a test reads by path -- via sandboxEntries,
// the exact same walk CopyModule itself uses (see its doc comment).
// Changing any such input and getting a cache hit with stale results
// was a real, found bug; the fix is structural (one shared file
// selection, not two independently maintained lists) so it can't
// silently reappear by CopyModule and Digest drifting apart again.
//
// A symlink is fingerprinted by its own link target string (via
// os.Readlink), never by dereferencing to the target's content:
// CopyModule recreates it as a symlink object pointing at that exact
// target, not a copy of whatever the target currently contains, so
// that target string is what actually changes the sandbox. If the
// target itself is a regular file inside root, it is walked and
// fingerprinted separately in its own right; this also means Digest
// never has to open a symlink's target at all, so a dangling or
// directory-target symlink elsewhere in the tree -- both of which
// CopyModule already tolerates, since recreating a symlink never
// touches what it points to -- can't turn into a hard Digest error.
func Digest(root, cacheDir string) (string, error) {
	type file struct {
		rel     string
		symlink bool
		target  string // set only when symlink is true
	}
	var files []file
	err := sandboxEntries(root, cacheDir, func(path, rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			files = append(files, file{rel: rel, symlink: true, target: target})
			return nil
		}
		files = append(files, file{rel: rel})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	h := sha256.New()
	for _, f := range files {
		_, _ = io.WriteString(h, f.rel)
		_, _ = h.Write([]byte{0})
		if f.symlink {
			_, _ = io.WriteString(h, "symlink:"+f.target)
			_, _ = h.Write([]byte{0})
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.rel)))
		if err != nil {
			return "", err
		}
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFile materializes dst as an independent copy of src with the given
// mode. It first attempts a copy-on-write clone via tryReflink (a no-op
// returning false on platforms/filesystems that don't support one, in
// which case this always falls back to the byte-for-byte copy below --
// see reflink_linux.go and reflink_other.go).
func copyFile(src, dst string, mode os.FileMode) error {
	if tryReflink(dst, src) {
		return os.Chmod(dst, mode.Perm())
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(out, in)
	closeErr := out.Close()
	if cpErr != nil {
		return cpErr
	}
	return closeErr
}
