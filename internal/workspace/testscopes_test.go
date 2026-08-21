package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// buildDepFixture reproduces the exact synthetic module used to verify
// this design empirically before it was implemented: package b imports
// a in its main code; package d's *external* test package (package
// d_test) imports a only from its _test.go file, simulating an
// integration-style test dependency that a naive walk of Imports (as
// opposed to asking `go list -deps -test` directly) would miss; package
// c is unrelated to either; package e mixes an internal and an external
// test file in the same package, each contributing its own dependency.
func buildDepFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":               "module depfixture\n\ngo 1.22\n",
		"a/a.go":               "package a\nfunc F() int { return 1 }\n",
		"a/a_test.go":          "package a\nimport \"testing\"\nfunc TestF(t *testing.T) { if F() != 1 { t.Fatal(\"bad\") } }\n",
		"b/b.go":               "package b\nimport \"depfixture/a\"\nfunc G() int { return a.F() + 1 }\n",
		"b/b_test.go":          "package b\nimport \"testing\"\nfunc TestG(t *testing.T) { if G() != 2 { t.Fatal(\"bad\") } }\n",
		"c/c.go":               "package c\nfunc H() int { return 99 }\n",
		"c/c_test.go":          "package c\nimport \"testing\"\nfunc TestH(t *testing.T) { if H() != 99 { t.Fatal(\"bad\") } }\n",
		"d/d.go":               "package d\nfunc I() int { return 7 }\n",
		"d/d_test.go":          "package d_test\nimport (\n\t\"testing\"\n\t\"depfixture/a\"\n)\nfunc TestIntegration(t *testing.T) { if a.F() != 1 { t.Fatal(\"bad\") } }\n",
		"e/e.go":               "package e\nfunc J() int { return 5 }\n",
		"e/e_internal_test.go": "package e\nimport \"testing\"\nfunc TestInternal(t *testing.T) { if J() != 5 { t.Fatal(\"bad\") } }\n",
		"e/e_external_test.go": "package e_test\nimport (\n\t\"testing\"\n\t\"depfixture/c\"\n)\nfunc TestExternal(t *testing.T) { if c.H() != 99 { t.Fatal(\"bad\") } }\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestTestScopesIncludesExternalTestOnlyDependency(t *testing.T) {
	root := buildDepFixture(t)
	scopes, err := TestScopes(root, []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}

	// A mutant in a's own tests, plus b (imports a directly) and d
	// (imports a only from its external _test.go package) must all be
	// in a's scope: this is the specific case a naive Imports-only walk
	// would get wrong.
	assertScope(t, scopes, "depfixture/a", []string{"depfixture/a", "depfixture/b", "depfixture/d"})

	// c is unrelated to a and must not appear in a's scope, nor should
	// a mutant in c pull in anything beyond c itself.
	assertScope(t, scopes, "depfixture/c", []string{"depfixture/c", "depfixture/e"})

	// b has no reverse dependents in this fixture: scope is itself only.
	assertScope(t, scopes, "depfixture/b", []string{"depfixture/b"})

	// e mixes an internal and an external test file; the external one
	// depends on c. Both must be unioned into e's own scope (e has no
	// reverse dependents itself).
	assertScope(t, scopes, "depfixture/e", []string{"depfixture/e"})
}

func TestTestScopesFindsCrossPackageDependencyEvenWhenNotDirectlyRequested(t *testing.T) {
	root := buildDepFixture(t)
	// Deliberately request only b and c, not a or d directly -- go list
	// -deps must still pull a in transitively as part of b's closure.
	scopes, err := TestScopes(root, []string{"./b/...", "./c/..."})
	if err != nil {
		t.Fatal(err)
	}
	assertScope(t, scopes, "depfixture/a", []string{"depfixture/b"})
}

func assertScope(t *testing.T, scopes map[string][]string, pkg string, want []string) {
	t.Helper()
	got := append([]string(nil), scopes[pkg]...)
	sort.Strings(got)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	if len(got) != len(sortedWant) {
		t.Fatalf("scope[%s] = %v, want %v", pkg, got, sortedWant)
	}
	for i := range got {
		if got[i] != sortedWant[i] {
			t.Fatalf("scope[%s] = %v, want %v", pkg, got, sortedWant)
		}
	}
}
