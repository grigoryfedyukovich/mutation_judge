package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestCopyModuleProducesIndependentCopy fills a real pre-existing gap:
// CopyModule was previously only exercised indirectly, through the
// higher-level analysis and CLI integration tests, with no unit test of
// its own. It checks the properties that matter regardless of whether
// the copy underneath happens to be a byte-for-byte copy or a
// copy-on-write clone (see reflink_linux.go): file content matches,
// modes are preserved, symlinks are preserved as symlinks, and .git /
// .mutation-judge / the configured cache directory are excluded.
func TestCopyModuleProducesIndependentCopy(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/m\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(root, "pkg", "p.go"), "package pkg\nfunc F() int { return 1 }\n", 0o600)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, ".git", "config"), "should not be copied", 0o644)
	mustWrite(t, filepath.Join(root, ".mutation-judge", "cache", "x.json"), "should not be copied", 0o644)
	if runtime.GOOS != "windows" {
		if err := os.Symlink("p.go", filepath.Join(root, "pkg", "link.go")); err != nil {
			t.Fatal(err)
		}
	}

	tmp, cleanup, err := CopyModule(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	got, err := os.ReadFile(filepath.Join(tmp, "pkg", "p.go"))
	if err != nil || string(got) != "package pkg\nfunc F() int { return 1 }\n" {
		t.Fatalf("copied content wrong: %q, err=%v", got, err)
	}
	info, err := os.Stat(filepath.Join(tmp, "pkg", "p.go"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(tmp, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git should have been excluded, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".mutation-judge")); !os.IsNotExist(err) {
		t.Fatalf(".mutation-judge should have been excluded, got err=%v", err)
	}
	if runtime.GOOS != "windows" {
		linkInfo, err := os.Lstat(filepath.Join(tmp, "pkg", "link.go"))
		if err != nil {
			t.Fatal(err)
		}
		if linkInfo.Mode()&os.ModeSymlink == 0 {
			t.Fatal("symlink was not preserved as a symlink")
		}
	}

	// The copy must be a fully independent file: writing to it must
	// never be observable in the original. This is the property that
	// matters most once CopyModule's underlying copy can be a
	// copy-on-write clone rather than a full byte copy -- see
	// reflink_linux_test.go for the same assertion made directly against
	// tryReflink, on a filesystem where a clone actually happens.
	if err := os.WriteFile(filepath.Join(tmp, "pkg", "p.go"), []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(root, "pkg", "p.go"))
	if err != nil || string(original) != "package pkg\nfunc F() int { return 1 }\n" {
		t.Fatalf("original was affected by a write to the copy: %q, err=%v", original, err)
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestApplyAndRestoreAtomically(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "p.go")
	original := []byte("package p\nvar N = 1\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	restore, err := Apply(root, "p.go", 18, 19, "2")
	if err != nil {
		t.Fatal(err)
	}
	mutated, _ := os.ReadFile(path)
	if string(mutated) != "package p\nvar N = 2\n" {
		t.Fatalf("unexpected mutation: %q", mutated)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatalf("restore mismatch: %q", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed: %v", info.Mode().Perm())
	}
}

func TestAtomicWriteFailureLeavesOriginalUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.go")
	original := []byte("original")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("rename failed")
	err := writeFileAtomicWithRename(path, []byte("mutated"), 0o644, func(_, _ string) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatalf("failed atomic write changed destination: %q", got)
	}
}

func TestApplyRejectsInvalidAndEscapingSpans(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		rel        string
		start, end int
	}{
		{"escape", "../outside.go", 0, 1},
		{"zero", "p.go", 2, 2},
		{"reversed", "p.go", 4, 2},
		{"past-end", "p.go", 0, 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Apply(root, tc.rel, tc.start, tc.end, "x"); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestApplyRejectsSymlinkOutsideSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "p.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(root, "p.go", 0, 1, "x"); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}

// TestSandboxEntriesVisitsExactlyWhatCopyModuleCopies pins the shared
// primitive both Digest and CopyModule are now built on: every file
// CopyModule would place into the sandbox is visited exactly once, and
// nothing under .git, .mutation-judge, or the configured (here,
// non-default) cache directory is. This is the structural half of the
// P0 fix -- Digest and CopyModule sharing one walk instead of two
// independently maintained file-selection functions -- so it's tested
// directly rather than only inferred from Digest's own behavior below.
func TestSandboxEntriesVisitsExactlyWhatCopyModuleCopies(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/m\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(root, "p.go"), "package p\n", 0o644)
	mustWrite(t, filepath.Join(root, "assets", "data.json"), `{"embedded":true}`, 0o644)
	mustWrite(t, filepath.Join(root, "native", "lib.c"), "int f(void) { return 1; }\n", 0o644)
	mustWrite(t, filepath.Join(root, "testdata", "fixture.txt"), "fixture v1", 0o644)
	mustWrite(t, filepath.Join(root, "go.env"), "GOFLAGS=-mod=mod\n", 0o644)
	mustWrite(t, filepath.Join(root, ".git", "config"), "not visited", 0o644)
	mustWrite(t, filepath.Join(root, ".mutation-judge", "cache", "x.json"), "not visited", 0o644)
	mustWrite(t, filepath.Join(root, "mycache", "results.json"), "not visited", 0o644)

	var visited []string
	if err := sandboxEntries(root, "mycache", func(_, rel string, d fs.DirEntry) error {
		if !d.IsDir() {
			visited = append(visited, rel)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(visited)
	want := []string{"assets/data.json", "go.env", "go.mod", "native/lib.c", "p.go", "testdata/fixture.txt"}
	if len(visited) != len(want) {
		t.Fatalf("visited = %v, want %v", visited, want)
	}
	for i := range want {
		if visited[i] != want[i] {
			t.Fatalf("visited = %v, want %v", visited, want)
		}
	}
}

// TestDigestCoversEverythingCopyModuleCopies is the permanent
// regression test for the P0 bug itself: Digest used to hash only
// *.go / go.mod / go.sum / go.work / go.work.sum, silently missing
// every other file CopyModule copies into the sandbox and a test can
// therefore observe. Changing any one of them -- an embedded asset, a
// cgo source, a testdata fixture, go.env -- while every hashed .go file
// stays byte-for-byte identical must still change the digest.
func TestDigestCoversEverythingCopyModuleCopies(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/m\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(root, "p.go"), "package p\n", 0o644)
	files := map[string]string{
		"assets/data.json":     `{"embedded":true}`,
		"native/lib.c":         "int f(void) { return 1; }\n",
		"native/lib.h":         "int f(void);\n",
		"testdata/fixture.txt": "fixture v1",
		"go.env":               "GOFLAGS=-mod=mod\n",
	}
	for rel, content := range files {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), content, 0o644)
	}

	base, err := Digest(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	for rel := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(original, []byte(" -- changed")...), 0o644); err != nil {
			t.Fatal(err)
		}
		changed, err := Digest(root, ".mutation-judge/cache")
		if err != nil {
			t.Fatal(err)
		}
		if changed == base {
			t.Fatalf("changing %s did not change the digest -- this file is invisible to the cache key despite being in the sandbox", rel)
		}
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDigestExcludesGitMutationJudgeAndConfiguredCacheDir mirrors
// TestCopyModuleProducesIndependentCopy's own exclusion assertions, but
// for Digest: none of these are part of what a test run observes (the
// cache directory in particular must be excluded, or every run would
// change its own digest the moment it writes a cache entry, making the
// cache permanently self-defeating), regardless of whether the cache
// directory happens to be the default ".mutation-judge/cache" or a
// custom path elsewhere in the tree.
func TestDigestExcludesGitMutationJudgeAndConfiguredCacheDir(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/m\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(root, "p.go"), "package p\n", 0o644)
	mustWrite(t, filepath.Join(root, ".git", "config"), "v1", 0o644)
	mustWrite(t, filepath.Join(root, "mycache", "results.json"), "v1", 0o644)

	base, err := Digest(root, "mycache")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, ".git", "config"), "v2 -- changed", 0o644)
	mustWrite(t, filepath.Join(root, "mycache", "results.json"), "v2 -- changed", 0o644)
	mustWrite(t, filepath.Join(root, "mycache", "new-entry.json"), "brand new cache entry", 0o644)
	after, err := Digest(root, "mycache")
	if err != nil {
		t.Fatal(err)
	}
	if after != base {
		t.Fatal("digest changed from writes inside .git or the configured cache directory -- neither is part of what a test observes")
	}
}

// TestDigestReflectsSymlinkRetargetingWithoutErroringOnDangling covers
// the symlink case deliberately left out of scope for this P0 (see
// ISSUES.md: outbound-symlink recreation is a separate, still-open
// item) but still owed a direct test here: CopyModule recreates a
// symlink as a symlink pointing at the same target string, never by
// copying whatever the target currently contains, so Digest must
// fingerprint that target string -- and, because it never dereferences
// the link to read through it, must not error out on a dangling
// symlink elsewhere in the tree (CopyModule already tolerates one: it
// only calls os.Readlink, never opens the target).
func TestDigestReflectsSymlinkRetargetingWithoutErroringOnDangling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/m\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(root, "a.txt"), "a", 0o644)
	mustWrite(t, filepath.Join(root, "b.txt"), "b", 0o644)
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink("a.txt", link); err != nil {
		t.Fatal(err)
	}

	base, err := Digest(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b.txt", link); err != nil {
		t.Fatal(err)
	}
	retargeted, err := Digest(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	if retargeted == base {
		t.Fatal("repointing a symlink to a different target did not change the digest")
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("does-not-exist.txt", link); err != nil {
		t.Fatal(err)
	}
	if _, err := Digest(root, ".mutation-judge/cache"); err != nil {
		t.Fatalf("a dangling symlink elsewhere in the tree must not fail Digest (CopyModule tolerates it too, since recreating a symlink never touches its target): %v", err)
	}
}

func TestSandboxSkipsOutboundSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/m\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(root, "inside.txt"), "in", 0o644)
	mustWrite(t, filepath.Join(outside, "secret.txt"), "secret", 0o644)
	if err := os.Symlink("inside.txt", filepath.Join(root, "ok.link")); err != nil {
		t.Fatal(err)
	}
	// absolute outbound
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "abs.out")); err != nil {
		t.Fatal(err)
	}
	// relative outbound
	if err := os.Symlink(filepath.Join("..", filepath.Base(outside), "secret.txt"), filepath.Join(root, "rel.out")); err != nil {
		// use path that climbs out of root
		if err := os.Symlink("../"+filepath.Base(outside)+"/secret.txt", filepath.Join(root, "rel.out")); err != nil {
			t.Fatal(err)
		}
	}

	tmp, cleanup, err := CopyModule(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Lstat(filepath.Join(tmp, "ok.link")); err != nil {
		t.Fatalf("internal symlink should be copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(tmp, "abs.out")); !os.IsNotExist(err) {
		t.Fatalf("absolute outbound symlink must be omitted, err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(tmp, "rel.out")); !os.IsNotExist(err) {
		t.Fatalf("relative outbound symlink must be omitted, err=%v", err)
	}

	// Digest must also ignore outbound links so they cannot poison the cache key
	base, err := Digest(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "abs.out")); err != nil {
		t.Fatal(err)
	}
	after, err := Digest(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	if after != base {
		t.Fatal("removing an outbound symlink changed the digest; outbound links must not be part of the fingerprint")
	}
}

func TestSandboxSkipsNodeModulesJunkVendorAndRootBin(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.test/m\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(root, "p.go"), "package p\n", 0o644)
	mustWrite(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "js", 0o644)
	mustWrite(t, filepath.Join(root, "bin", "tool"), "#!/bin/sh\n", 0o755)
	mustWrite(t, filepath.Join(root, "vendor", "readme.txt"), "not a real vendor tree", 0o644)
	// real vendor tree must be kept
	mustWrite(t, filepath.Join(root, "vendor", "modules.txt"), "# example.com/x\n", 0o644)
	mustWrite(t, filepath.Join(root, "vendor", "example.com", "x", "x.go"), "package x\n", 0o644)
	// bin that is a Go package must be kept
	mustWrite(t, filepath.Join(root, "cmdish", "bin", "main.go"), "package main\n", 0o644)

	// Rebuild vendor-less junk path: use separate dir name for false vendor
	// (we already have real vendor with modules.txt). Add fake at sub/vendor without modules.txt
	mustWrite(t, filepath.Join(root, "third_party", "vendor", "orphan.txt"), "orphan", 0o644)

	tmp, cleanup, err := CopyModule(root, ".mutation-judge/cache")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(tmp, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("node_modules should be skipped, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "bin")); !os.IsNotExist(err) {
		t.Fatalf("root bin/ without .go files should be skipped, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "vendor", "modules.txt")); err != nil {
		t.Fatalf("real vendor/ must be kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "third_party", "vendor")); !os.IsNotExist(err) {
		t.Fatalf("vendor without modules.txt should be skipped, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "cmdish", "bin", "main.go")); err != nil {
		t.Fatalf("non-root bin package must be kept: %v", err)
	}
}

func TestParseGoWorkUsePaths(t *testing.T) {
	dir := t.TempDir()
	single := filepath.Join(dir, "single.work")
	mustWrite(t, single, "go 1.22\n\nuse ./mod\n", 0o644)
	got, err := parseGoWorkUsePaths(single)
	if err != nil || len(got) != 1 || got[0] != "./mod" {
		t.Fatalf("single: got %v err=%v", got, err)
	}
	multi := filepath.Join(dir, "multi.work")
	mustWrite(t, multi, "go 1.22\n\nuse (\n\t./a\n\t./b // comment\n)\n", 0o644)
	got, err = parseGoWorkUsePaths(multi)
	if err != nil || len(got) != 2 {
		t.Fatalf("multi: got %v err=%v", got, err)
	}
}

func TestRejectMultiModuleWorkspace(t *testing.T) {
	// Build a synthetic multi-module workspace and ensure ModuleRoot refuses it.
	base := t.TempDir()
	modA := filepath.Join(base, "a")
	modB := filepath.Join(base, "b")
	mustWrite(t, filepath.Join(modA, "go.mod"), "module example.test/a\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(modA, "a.go"), "package a\n", 0o644)
	mustWrite(t, filepath.Join(modB, "go.mod"), "module example.test/b\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(modB, "b.go"), "package b\n", 0o644)
	work := filepath.Join(base, "go.work")
	mustWrite(t, work, "go 1.22\n\nuse (\n\t./a\n\t./b\n)\n", 0o644)

	t.Setenv("GOWORK", work)
	_, err := ModuleRoot(modA)
	if err == nil {
		t.Fatal("expected multi-module workspace to be rejected")
	}
	if !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("error should mention workspace, got %v", err)
	}

	t.Setenv("GOWORK", "off")
	root, err := ModuleRoot(modA)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(root) != filepath.Clean(modA) {
		t.Fatalf("root=%s want %s", root, modA)
	}
}

func TestGoWorkReplaceCount(t *testing.T) {
	dir := t.TempDir()
	none := filepath.Join(dir, "none.work")
	mustWrite(t, none, "go 1.22\n\nuse ./mod\n", 0o644)
	if n, err := goWorkReplaceCount(none); err != nil || n != 0 {
		t.Fatalf("none: got %d err=%v", n, err)
	}

	single := filepath.Join(dir, "single.work")
	mustWrite(t, single, "go 1.22\n\nuse ./mod\n\nreplace example.com/foo => ../foo-fork\n", 0o644)
	if n, err := goWorkReplaceCount(single); err != nil || n != 1 {
		t.Fatalf("single: got %d err=%v", n, err)
	}

	block := filepath.Join(dir, "block.work")
	mustWrite(t, block, "go 1.22\n\nuse ./mod\n\nreplace (\n\texample.com/foo => ../foo-fork\n\texample.com/bar => ../bar-fork // comment\n)\n", 0o644)
	if n, err := goWorkReplaceCount(block); err != nil || n != 2 {
		t.Fatalf("block: got %d err=%v", n, err)
	}
}

// TestRejectWorkspaceWithReplace pins down the gap TestRejectMultiModuleWorkspace
// cannot: a go.work that lists exactly one module (so the multi-module
// check alone would let it through) but also carries a replace
// directive, which can rewrite dependency resolution the same way a
// module-level replace would -- invisibly to workspace.Digest, since
// Digest only ever hashes files under the module root itself, never
// the go.work file that lives beside/above it. Same module bytes,
// different active go.work replace, would otherwise digest identically
// and share a cache entry despite testing different resolved
// dependencies.
func TestRejectWorkspaceWithReplace(t *testing.T) {
	base := t.TempDir()
	mod := filepath.Join(base, "a")
	mustWrite(t, filepath.Join(mod, "go.mod"), "module example.test/a\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(mod, "a.go"), "package a\n", 0o644)
	work := filepath.Join(base, "go.work")
	mustWrite(t, work, "go 1.22\n\nuse ./a\n\nreplace example.com/foo => ../foo-fork\n", 0o644)

	t.Setenv("GOWORK", work)
	_, err := ModuleRoot(mod)
	if err == nil {
		t.Fatal("expected replace-bearing single-module workspace to be rejected")
	}
	if !strings.Contains(err.Error(), "replace") {
		t.Fatalf("error should mention replace, got %v", err)
	}
}

// TestSingleModuleWorkspaceWithoutReplaceIsAllowed exercises an actually
// active single-module go.work (unlike TestRejectMultiModuleWorkspace's
// second case, which only checks GOWORK=off) to confirm
// rejectUnsafeWorkspace's stated safe case is genuinely accepted, not
// just untested.
func TestSingleModuleWorkspaceWithoutReplaceIsAllowed(t *testing.T) {
	base := t.TempDir()
	mod := filepath.Join(base, "a")
	mustWrite(t, filepath.Join(mod, "go.mod"), "module example.test/a\n\ngo 1.22\n", 0o644)
	mustWrite(t, filepath.Join(mod, "a.go"), "package a\n", 0o644)
	work := filepath.Join(base, "go.work")
	mustWrite(t, work, "go 1.22\n\nuse ./a\n", 0o644)

	t.Setenv("GOWORK", work)
	root, err := ModuleRoot(mod)
	if err != nil {
		t.Fatalf("expected single-module, replace-free workspace to be allowed: %v", err)
	}
	if filepath.Clean(root) != filepath.Clean(mod) {
		t.Fatalf("root=%s want %s", root, mod)
	}
}
