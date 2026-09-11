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
	root := filepath.Dir(gomod)
	if err := rejectUnsafeWorkspace(cwd, root); err != nil {
		return "", err
	}
	return root, nil
}

// rejectUnsafeWorkspace fails when an active go.work (GOWORK not empty
// and not "off") does anything a single-module sandbox copy cannot
// faithfully represent:
//
//   - lists more than one module in `use`: dependency resolution and
//     package patterns can differ from analyzing the one module root
//     mutation-judge copies and digests.
//   - contains any `replace` directive: a workspace-level replace can
//     silently rewrite which source a dependency resolves to, exactly
//     as a module-level replace would, but it lives in a file outside
//     the module root entirely. workspace.Digest hashes only the
//     module root's own files (see CopyModule/Digest's doc comments),
//     so two otherwise-identical checkouts with different active
//     go.work replace directives digest identically and share a cache
//     entry despite testing different resolved dependencies -- same
//     module bytes, different GOWORK, cache hit, different tests.
//     Rather than fingerprinting an arbitrary, possibly-relative,
//     possibly-out-of-module replace target (and risk that
//     fingerprint drifting out of sync with what Digest/CopyModule
//     actually do to the sandbox, which is exactly how they drifted
//     apart from each other before -- see ISSUES.md), a
//     replace-bearing workspace is refused outright with the same
//     remediation as the multi-module case: re-run with GOWORK=off.
//
// A go.work with neither problem (a single `use` entry and no
// replace) is allowed and is not itself further special-cased:
// runner.GoTest.Run always forces GOWORK=off for the sandboxed test
// process, since that process's cmd.Dir is a temporary copy of the
// module root alone (see CopyModule) and never one of go.work's
// use-listed directories -- leaving a workspace active there would
// either resolve dependencies from outside the sandbox entirely or
// make `go test` refuse to run as "not in any workspace module". So a
// go.work that survives this check is, for every purpose this tool
// cares about, already indistinguishable from no go.work at all.
func rejectUnsafeWorkspace(cwd, moduleRoot string) error {
	cmd := exec.Command("go", "env", "GOWORK")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go env GOWORK failed: %w", err)
	}
	gowork := strings.TrimSpace(string(out))
	if gowork == "" || gowork == "off" {
		return nil
	}
	uses, err := parseGoWorkUsePaths(gowork)
	if err != nil {
		return fmt.Errorf("reading go.work %s: %w", gowork, err)
	}
	if len(uses) > 1 {
		return fmt.Errorf("go workspace %s lists %d modules; mutation-judge analyzes a single module root (%s). Re-run with GOWORK=off from that module, or reduce the workspace to one module", gowork, len(uses), moduleRoot)
	}
	nreplace, err := goWorkReplaceCount(gowork)
	if err != nil {
		return fmt.Errorf("reading go.work %s: %w", gowork, err)
	}
	if nreplace > 0 {
		word := "directive"
		if nreplace != 1 {
			word = "directives"
		}
		return fmt.Errorf("go workspace %s has %d replace %s; mutation-judge analyzes a single module root (%s) and cannot reflect a workspace-level replace in its cache key or sandbox copy. Re-run with GOWORK=off from that module", gowork, nreplace, word, moduleRoot)
	}
	return nil
}

// goWorkReplaceCount returns the number of replace directives in a
// go.work file, in both the single-line `replace old => new` and
// parenthesized `replace (\n\t...\n)` forms. It only needs a count (to
// report and to gate on >0), never the replaced paths themselves --
// see rejectUnsafeWorkspace for why those paths are deliberately never
// parsed, resolved, or fingerprinted here.
func goWorkReplaceCount(workFile string) (int, error) {
	b, err := os.ReadFile(workFile)
	if err != nil {
		return 0, err
	}
	n := 0
	inReplaceBlock := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if inReplaceBlock {
			if line == ")" {
				inReplaceBlock = false
				continue
			}
			n++
			continue
		}
		if line == "replace (" {
			inReplaceBlock = true
			continue
		}
		if strings.HasPrefix(line, "replace ") {
			n++
		}
	}
	return n, nil
}

// parseGoWorkUsePaths returns the path arguments of every use directive
// in a go.work file. It handles both `use ./foo` and parenthesized
// `use (\n\t./foo\n)` forms; replace/go lines are ignored.
func parseGoWorkUsePaths(workFile string) ([]string, error) {
	b, err := os.ReadFile(workFile)
	if err != nil {
		return nil, err
	}
	var uses []string
	inUseBlock := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if inUseBlock {
			if line == ")" {
				inUseBlock = false
				continue
			}
			// strip trailing comments
			if i := strings.Index(line, " //"); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			if line != "" {
				uses = append(uses, line)
			}
			continue
		}
		if line == "use (" {
			inUseBlock = true
			continue
		}
		if strings.HasPrefix(line, "use ") {
			arg := strings.TrimSpace(strings.TrimPrefix(line, "use "))
			if arg == "(" {
				inUseBlock = true
				continue
			}
			if i := strings.Index(arg, " //"); i >= 0 {
				arg = strings.TrimSpace(arg[:i])
			}
			if arg != "" {
				uses = append(uses, arg)
			}
		}
	}
	return uses, nil
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
// order, and skipping exactly the same directories and outbound
// symlinks -- invoking fn once for every directory and non-directory
// entry CopyModule would otherwise place into the sandbox. This is the
// single shared source of truth for "what will a test run inside the
// sandbox actually see": Digest and CopyModule independently walking
// the tree with their own skip/include rules is exactly how they
// drifted apart before (see ISSUES.md). Sharing this walk makes that
// class of drift structurally impossible to reintroduce.
//
// Always skipped directories:
//   - .git, .mutation-judge, the configured cache directory
//   - any directory named node_modules (not part of a Go build)
//   - vendor/ when vendor/modules.txt is absent (not a real Go vendor tree)
//   - module-root bin/ when it contains no *.go files (built binaries only)
//
// vendor/ with modules.txt is always included: -mod=vendor and tests that
// read vendored sources by path would otherwise see a different tree.
//
// Symlinks whose resolved target path would leave the module root are
// skipped (not recreated in the sandbox and not fingerprinted). Internal
// and dangling-but-in-tree-target symlinks are still visited; see
// symlinkTargetInsideRoot.
func sandboxEntries(root, cacheDir string, fn func(path, rel string, d fs.DirEntry) error) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cacheAbs := cacheDir
	if !filepath.IsAbs(cacheAbs) {
		cacheAbs = filepath.Join(rootAbs, cacheDir)
	}
	cacheAbs, err = filepath.Abs(cacheAbs)
	if err != nil {
		return err
	}
	return filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == rootAbs {
			return nil
		}
		if d.IsDir() {
			if skipSandboxDir(rootAbs, cacheAbs, path, d.Name()) {
				return filepath.SkipDir
			}
		} else if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if !symlinkTargetInsideRoot(rootAbs, path, target) {
				return nil // outbound: omit from sandbox and digest
			}
		}
		rel, err := filepath.Rel(rootAbs, path)
		if err != nil {
			return err
		}
		return fn(path, filepath.ToSlash(rel), d)
	})
}

// skipSandboxDir reports whether path should be excluded from the
// sandbox entirely. rootAbs and cacheAbs must be absolute.
func skipSandboxDir(rootAbs, cacheAbs, path, name string) bool {
	if name == ".git" || name == ".mutation-judge" || filepath.Clean(path) == cacheAbs {
		return true
	}
	if name == "node_modules" {
		return true
	}
	if name == "vendor" {
		if _, err := os.Stat(filepath.Join(path, "modules.txt")); err != nil {
			return true // not a real Go vendor tree
		}
		return false
	}
	if name == "bin" {
		rel, err := filepath.Rel(rootAbs, path)
		if err == nil && filepath.ToSlash(rel) == "bin" && !dirContainsGoFiles(path) {
			return true
		}
	}
	return false
}

// dirContainsGoFiles reports whether any direct child of dir is a *.go
// file (non-recursive). Used only to decide whether root-level bin/ is
// a Go package vs a directory of built binaries.
func dirContainsGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// symlinkTargetInsideRoot reports whether a symlink at linkPath with
// the given target string would resolve under rootAbs. The target need
// not exist (dangling links whose path would still lie inside the
// module are kept). Absolute targets and relative targets that climb
// out of the module are rejected so the sandbox never re-creates a
// host-escape link.
func symlinkTargetInsideRoot(rootAbs, linkPath, target string) bool {
	var abs string
	if filepath.IsAbs(target) {
		abs = filepath.Clean(target)
	} else {
		abs = filepath.Clean(filepath.Join(filepath.Dir(linkPath), target))
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
// that target string is what actually changes the sandbox. Outbound
// symlinks (targets that resolve outside the module root) are omitted
// by sandboxEntries on both the Digest and CopyModule paths, so they
// neither affect the cache key nor reappear as host-escape links in
// the sandbox. Internal dangling links are still fingerprinted and
// recreated.
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
