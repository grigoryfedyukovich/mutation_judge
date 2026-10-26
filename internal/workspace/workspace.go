package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/example/mutation-judge/internal/proc"
)

func ModuleRoot(cwd string) (string, error) {
	return ModuleRootContext(context.Background(), cwd)
}

// ModuleRootContext resolves the module without outliving the analysis request.
func ModuleRootContext(ctx context.Context, cwd string) (string, error) {
	cmd := proc.Command(ctx, "go", "env", "GOMOD")
	cmd.Dir = cwd
	out, err := proc.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("go env GOMOD failed: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull || gomod == "/dev/null" {
		return "", fmt.Errorf("no Go module found from %s", cwd)
	}
	root := filepath.Dir(gomod)
	if err := rejectUnsafeWorkspaceContext(ctx, cwd, root); err != nil {
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
	return rejectUnsafeWorkspaceContext(context.Background(), cwd, moduleRoot)
}

func rejectUnsafeWorkspaceContext(ctx context.Context, cwd, moduleRoot string) error {
	cmd := proc.Command(ctx, "go", "env", "GOWORK")
	cmd.Dir = cwd
	out, err := proc.Output(cmd)
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
// go.work file, via goWorkDirectiveArgs.
func goWorkReplaceCount(workFile string) (int, error) {
	args, err := goWorkDirectiveArgs(workFile, "replace")
	if err != nil {
		return 0, err
	}
	return len(args), nil
}

// goWorkDirectiveArgs returns, for a given directive verb ("use" or
// "replace"), every argument line that verb applies to -- both the
// single-line `verb arg` form and the parenthesized block form.
//
// This replaces an earlier version that matched fixed literal
// substrings ("use (", " //") instead of tokenizing "(" as its own
// token: that let two syntactically ordinary go.work files produce
// zero recognized entries and slip straight past
// rejectUnsafeWorkspace's len(uses)<=1 check as if they were
// single-module, when they were not --
//
//   - `use(` with no space before the paren (cutVerb below now
//     accepts "(" as a token boundary the same as whitespace, exactly
//     as the real go.work grammar does)
//   - `use ( // comment` -- a trailing line comment on the same
//     physical line as the opening paren (comments are now stripped
//     before the "did this line just open a block" check runs, not
//     compared against literally instead of after stripping)
//
// It still does not implement the full modfile grammar: no quoted-string
// escaping and no `/* */` block comments, since go.work paths are
// essentially never quoted and block comments do not appear in any
// fixture or real-world go.work this tool has been run against.
// Deliberately narrow, but tokenized rather than string-matched for
// the part that actually matters here: whether "(" opens a block,
// regardless of the whitespace and comments incidentally sitting
// next to it.
func goWorkDirectiveArgs(workFile, verb string) ([]string, error) {
	b, err := os.ReadFile(workFile)
	if err != nil {
		return nil, err
	}
	var args []string
	inBlock := false
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(stripLineComment(raw))
		if line == "" {
			continue
		}
		if inBlock {
			if line == ")" {
				inBlock = false
				continue
			}
			args = append(args, line)
			continue
		}
		rest, ok := cutVerb(line, verb)
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "(" {
			inBlock = true
			continue
		}
		if rest != "" {
			args = append(args, rest)
		}
	}
	return args, nil
}

// stripLineComment removes a trailing "//" line comment. It does not
// need to understand quoted strings for this tool's purposes -- see
// goWorkDirectiveArgs's doc comment.
func stripLineComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		return line[:i]
	}
	return line
}

// cutVerb reports whether line begins with verb as its own token --
// followed by whitespace, "(", or end of line, never merely a
// same-prefix identifier such as "usex" -- and returns what follows.
func cutVerb(line, verb string) (rest string, ok bool) {
	if !strings.HasPrefix(line, verb) {
		return "", false
	}
	after := line[len(verb):]
	if after == "" {
		return "", true
	}
	switch after[0] {
	case ' ', '\t', '(':
		return after, true
	default:
		return "", false
	}
}

// parseGoWorkUsePaths returns the path arguments of every use directive
// in a go.work file, via goWorkDirectiveArgs (see its doc comment for
// what it does and does not handle).
func parseGoWorkUsePaths(workFile string) ([]string, error) {
	return goWorkDirectiveArgs(workFile, "use")
}

type Package struct {
	Dir          string
	GoFiles      []string
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
	ImportPath   string
	Deps         []string
	ForTest      string
	Error        *struct{ Err string }
}

// HasOwnTests reports whether p has any test files of its own -- an
// internal (`package foo`) or external (`package foo_test`) test file
// declared directly in this package's directory. It says nothing about
// whether some *other* package's tests exercise p's code.
func (p Package) HasOwnTests() bool {
	return len(p.TestGoFiles) > 0 || len(p.XTestGoFiles) > 0
}

func ListPackages(cwd string, patterns []string) ([]Package, error) {
	return ListPackagesContext(context.Background(), cwd, patterns)
}

func ListPackagesContext(ctx context.Context, cwd string, patterns []string) ([]Package, error) {
	args := append([]string{"list", "-json", "-e"}, patterns...)
	cmd := proc.Command(ctx, "go", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := proc.Output(cmd)
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
	return TestScopesContext(context.Background(), cwd, patterns)
}

func TestScopesContext(ctx context.Context, cwd string, patterns []string) (map[string][]string, error) {
	args := append([]string{"list", "-json", "-deps", "-test", "-e"}, patterns...)
	cmd := proc.Command(ctx, "go", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := proc.Output(cmd)
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
//   - the module root's own vendor/ when vendor/modules.txt is absent
//     (not a real Go vendor tree); vendor/ directories anywhere else in
//     the tree are ordinary directories -- see skipSandboxDir -- and are
//     never skipped by name alone
//   - module-root bin/ when it contains no *.go files (built binaries only)
//
// vendor/ with modules.txt is always included: -mod=vendor and tests that
// read vendored sources by path would otherwise see a different tree.
//
// Symlinks whose resolved target path would leave the module root are
// skipped (not recreated in the sandbox and not fingerprinted). Internal
// and dangling-but-in-tree-target symlinks are still visited; see
// symlinkTargetInsideRoot. Absolute targets that resolve inside the
// module are rewritten on copy to a path relative to the sandbox link
// (see sandboxSymlinkTarget) so tests cannot read the host file through
// the link while Apply mutates the sandbox copy.
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
		rel, err := filepath.Rel(rootAbs, path)
		if err != nil || filepath.ToSlash(rel) != "vendor" {
			// Not the module root's own vendor/. Go's vendor
			// consistency mechanism (-mod=vendor, vendor/modules.txt)
			// applies only to the main module's top-level vendor/; a
			// directory that merely happens to be named "vendor"
			// deeper in the tree (cmd/vendor, pkg/vendor,
			// third_party/vendor, ...) is, as far as go build/go test
			// are concerned, an entirely ordinary directory -- it can
			// be a real Go package, imported by name from elsewhere in
			// the module, with no modules.txt of its own. Skipping it
			// on name alone dropped that package from both the sandbox
			// copy and the digest, so anything importing it built fine
			// on the host but failed (INVALID, missing package) in the
			// sandbox. Only the module root's own vendor/ gets the
			// modules.txt check below; everywhere else, "vendor" is
			// just a directory name.
			return false
		}
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

// sandboxSymlinkTarget is the target string CopyModule writes into the
// sandbox for a kept symlink. Relative targets are copied as-is (they
// already resolve inside the copied tree). Absolute targets that
// symlinkTargetInsideRoot already accepted still point at the *host*
// path -- recreating that string would let a sandbox test read the
// unmutated original while Apply only changes the copy. Rewrite those
// to a path relative to the sandbox link, aiming at the same module-
// relative file in the sandbox.
func sandboxSymlinkTarget(rootAbs, sandboxRoot, dst, target string) (string, error) {
	if !filepath.IsAbs(target) {
		return target, nil
	}
	relFromRoot, err := filepath.Rel(rootAbs, filepath.Clean(target))
	if err != nil {
		return "", err
	}
	dest := filepath.Join(sandboxRoot, relFromRoot)
	return filepath.Rel(filepath.Dir(dst), dest)
}

// fingerprintSymlinkTarget is what Digest hashes for a kept symlink.
// Relative targets are the Readlink string (same as the sandbox
// recreation). Absolute in-tree targets are hashed as "abs:" plus the
// slash-separated path relative to the module root -- the same identity
// sandboxSymlinkTarget rewrites to -- so a checkout at a different
// absolute prefix does not churn the cache key, and retargeting the
// link to a different in-module file still does.
func fingerprintSymlinkTarget(rootAbs, target string) string {
	if !filepath.IsAbs(target) {
		return target
	}
	rel, err := filepath.Rel(rootAbs, filepath.Clean(target))
	if err != nil {
		return target
	}
	return "abs:" + filepath.ToSlash(rel)
}

func CopyModule(root, cacheDir string) (string, func(), error) {
	return CopyModuleContext(context.Background(), root, cacheDir)
}

// CopyModuleContext checks cancellation between entries and while copying
// large files during preparation. Sandboxes are removed on cancellation.
func CopyModuleContext(ctx context.Context, root, cacheDir string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "mutation-judge-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	err = sandboxEntries(root, cacheDir, func(path, rel string, d fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dst := filepath.Join(tmp, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			rewritten, err := sandboxSymlinkTarget(rootAbs, tmp, dst, target)
			if err != nil {
				return err
			}
			return os.Symlink(rewritten, dst)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported sandbox entry type %s: %s", info.Mode().Type(), rel)
		}
		return copyFileContext(ctx, path, dst, info.Mode())
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp, cleanup, nil
}

func Apply(root string, rel string, start, end int, replacement string) (func() error, error) {
	return apply(root, rel, start, end, "", replacement, false)
}

// ApplyChecked refuses stale or mismatched mutation spans. The expected
// original bytes come from discovery against the immutable snapshot.
func ApplyChecked(root, rel string, start, end int, expected, replacement string) (func() error, error) {
	return apply(root, rel, start, end, expected, replacement, true)
}

func apply(root, rel string, start, end int, expected, replacement string, checkOriginal bool) (func() error, error) {
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
	if checkOriginal && string(original[start:end]) != expected {
		return nil, fmt.Errorf("mutation span %d:%d in %s differs from discovered original %q (got %q)", start, end, rel, expected, string(original[start:end]))
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
// A symlink is fingerprinted by its own link target, never by
// dereferencing to the target's content: CopyModule recreates a
// symlink object, not a copy of whatever the target currently
// contains. Relative targets are hashed as the Readlink string (the
// same string written into the sandbox). Absolute in-tree targets are
// hashed as "abs:" plus the module-relative path -- matching the
// rewrite CopyModule applies -- so a different checkout prefix does
// not churn the cache, and retargeting still does. Outbound
// symlinks are omitted by sandboxEntries on both paths.
func Digest(root, cacheDir string) (string, error) {
	type entry struct {
		rel    string
		kind   string
		mode   os.FileMode
		target string
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	var entries []entry
	err = sandboxEntries(root, cacheDir, func(path, rel string, d fs.DirEntry) error {
		if d.IsDir() {
			// CopyModule materializes all directories with mode 0755.
			entries = append(entries, entry{rel: rel, kind: "directory"})
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entries = append(entries, entry{rel: rel, kind: "symlink", target: fingerprintSymlinkTarget(rootAbs, target)})
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported digest entry type %s: %s", info.Mode().Type(), rel)
		}
		entries = append(entries, entry{rel: rel, kind: "file", mode: info.Mode().Perm()})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	h := sha256.New()
	buf := make([]byte, 32<<10) // reused for every file, independent of source size
	for _, e := range entries {
		_, _ = io.WriteString(h, e.rel)
		_, _ = h.Write([]byte{0})
		_, _ = io.WriteString(h, e.kind)
		_, _ = h.Write([]byte{0})
		switch e.kind {
		case "symlink":
			_, _ = io.WriteString(h, e.target)
		case "file":
			_, _ = io.WriteString(h, fmt.Sprintf("%04o", e.mode))
			_, _ = h.Write([]byte{0})
			// Hash file contents in bounded chunks instead of allocating the
			// entire file. The bytes fed to h are identical to os.ReadFile,
			// preserving existing cache identities for unchanged trees.
			f, err := os.Open(filepath.Join(root, filepath.FromSlash(e.rel)))
			if err != nil {
				return "", err
			}
			_, copyErr := io.CopyBuffer(h, f, buf)
			closeErr := f.Close()
			if copyErr != nil {
				return "", fmt.Errorf("hash %s: %w", e.rel, copyErr)
			}
			if closeErr != nil {
				return "", fmt.Errorf("close hashed file %s: %w", e.rel, closeErr)
			}
		}
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HasLocalReplacements reads the Go toolchain's parsed go.mod syntax rather
// than guessing at quoted paths and parenthesized replace blocks. A local
// replacement, even an absolute path beneath root, is resolved by go test
// independently of the module snapshot and is not a safe cache input.
func HasLocalReplacements(root string) (bool, error) {
	return HasLocalReplacementsContext(context.Background(), root)
}

func HasLocalReplacementsContext(ctx context.Context, root string) (bool, error) {
	cmd := proc.Command(ctx, "go", "mod", "edit", "-json")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	b, err := proc.Output(cmd)
	if err != nil {
		return false, fmt.Errorf("go mod edit -json: %w", err)
	}
	var parsed struct {
		Replace []struct {
			New struct {
				Path    string `json:"Path"`
				Version string `json:"Version"`
			} `json:"New"`
		} `json:"Replace"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return false, fmt.Errorf("parse go mod edit JSON: %w", err)
	}
	for _, r := range parsed.Replace {
		if r.New.Path != "" && r.New.Version == "" {
			return true, nil
		}
	}
	return false, nil
}

// copyFile materializes dst as an independent copy of src with the given
// mode. It first attempts a copy-on-write clone via tryReflink (a no-op
// returning false on platforms/filesystems that don't support one, in
// which case this always falls back to the byte-for-byte copy below --
// see reflink_linux.go and reflink_other.go).
func copyFile(src, dst string, mode os.FileMode) error {
	return copyFileContext(context.Background(), src, dst, mode)
}

func copyFileContext(ctx context.Context, src, dst string, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	buf := make([]byte, 32<<10)
	var cpErr error
	for {
		if cpErr = ctx.Err(); cpErr != nil {
			break
		}
		var n int
		n, cpErr = in.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				cpErr = err
				break
			}
		}
		if cpErr == io.EOF {
			cpErr = nil
			break
		}
		if cpErr != nil {
			break
		}
	}
	closeErr := out.Close()
	if cpErr != nil {
		return cpErr
	}
	return closeErr
}
