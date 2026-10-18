package coverage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parseF12(t *testing.T, data string) (Map, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile.out")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return Parse(path, t.TempDir())
}

// Register zero-hit files even when not one block was executed. Presence
// (instrumented file) and positive execution evidence are distinct facts.
func TestF12ZeroCountAndAbsentFiles(t *testing.T) {
	m, err := parseF12(t, "mode: count\nexample.test/p/a.go:1.1,5.3 1 0\nexample.test/p/b.go:2.1,4.3 1 2\nexample.test/p/b.go:6.1,7.3 1 0\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file       string
		start, end int
		covered    bool
		known      bool
	}{
		{"a.go", 1, 5, false, true},
		{"b.go", 2, 4, true, true},
		{"b.go", 6, 7, false, true},
		{"missing.go", 1, 5, false, false},
	} {
		gotCovered, gotKnown := m.Covered(tc.file, tc.start, tc.end)
		if gotCovered != tc.covered || gotKnown != tc.known {
			t.Errorf("Covered(%s, %d, %d) = (%t, %t), want (%t, %t)", tc.file, tc.start, tc.end, gotCovered, gotKnown, tc.covered, tc.known)
		}
	}
}

func TestF12ZeroCountAmbiguousSuffixRemainsUnknown(t *testing.T) {
	m, err := parseF12(t, "mode: set\nexample.test/a/shared/p.go:1.1,2.3 1 0\nexample.test/b/shared/p.go:1.1,2.3 1 0\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, known := m.Covered("shared/p.go", 1, 1); known {
		t.Fatal("even identical zero-count files must retain suffix ambiguity")
	}
}

func TestF12ValidModesAndHeaderOnly(t *testing.T) {
	for _, mode := range []string{"set", "count", "atomic"} {
		t.Run(mode, func(t *testing.T) {
			m, err := parseF12(t, "mode: "+mode+"\nexample.test/p/p.go:1.1,2.3 0 0\n")
			if err != nil {
				t.Fatal(err)
			}
			if covered, known := m.Covered("p.go", 1, 1); !known || covered {
				t.Fatalf("valid zero-counter block: covered=%t known=%t", covered, known)
			}
		})
	}
	m, err := parseF12(t, "mode: set\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, known := m.Covered("p.go", 1, 1); known {
		t.Fatal("header-only profile does not establish instrumentation of any file")
	}
}

func TestF12RejectMalformedProfile(t *testing.T) {
	const good = "example.test/p/p.go:1.1,2.3 1 1\n"
	cases := map[string]string{
		"empty":                   "",
		"missing header":          good,
		"invalid header":          "mode: questionable\n" + good,
		"header after data":       "mode: set\n" + good + "mode: count\n",
		"missing statement field": "mode: set\nexample.test/p/p.go:1.1,2.3 1\n",
		"extra tokens":            "mode: set\nexample.test/p/p.go:1.1,2.3 1 1 garbage\n",
		"blank row":               "mode: set\n\n" + good,
		"missing file":            "mode: set\n:1.1,2.3 1 1\n",
		"missing colon":           "mode: set\nexample.test/p/p.go,2.3 1 1\n",
		"bad start line":          "mode: set\nexample.test/p/p.go:bad.1,2.3 1 1\n",
		"overflow start line":     "mode: set\nexample.test/p/p.go:999999999999999999999.1,2.3 1 1\n",
		"zero start line":         "mode: set\nexample.test/p/p.go:0.1,2.3 1 1\n",
		"negative end column":     "mode: set\nexample.test/p/p.go:1.1,2.-3 1 1\n",
		"zero end column":         "mode: set\nexample.test/p/p.go:1.1,2.0 1 1\n",
		"bad end line":            "mode: set\nexample.test/p/p.go:1.1,bad.3 1 1\n",
		"bad range syntax":        "mode: set\nexample.test/p/p.go:1.1;2.3 1 1\n",
		"extra comma":             "mode: set\nexample.test/p/p.go:1.1,2.3,3.4 1 1\n",
		"reversed range":          "mode: set\nexample.test/p/p.go:5.1,2.3 1 1\n",
		"empty range":             "mode: set\nexample.test/p/p.go:2.3,2.3 1 1\n",
		"huge span":               "mode: set\nexample.test/p/p.go:1.1,999999999.2 1 1\n",
		"invalid statements":      "mode: set\nexample.test/p/p.go:1.1,2.3 nope 1\n",
		"negative statements":     "mode: set\nexample.test/p/p.go:1.1,2.3 -1 1\n",
		"overflow statements":     "mode: set\nexample.test/p/p.go:1.1,2.3 999999999999999999999 1\n",
		"invalid count":           "mode: set\nexample.test/p/p.go:1.1,2.3 1 nope\n",
		"negative count":          "mode: count\nexample.test/p/p.go:1.1,2.3 1 -1\n",
		"overflow count":          "mode: count\nexample.test/p/p.go:1.1,2.3 1 999999999999999999999\n",
		"invalid set count":       "mode: set\nexample.test/p/p.go:1.1,2.3 1 2\n",
		"bad second row":          "mode: count\n" + good + "example.test/p/p.go:5.3,? 1 1\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			m, err := parseF12(t, input)
			if err == nil {
				t.Fatalf("accepted malformed profile (%s): %#v", name, m)
			}
			if !strings.Contains(err.Error(), "coverage profile") {
				t.Fatalf("expected contextual coverage profile error, got %v", err)
			}
		})
	}
}

func TestF12PerTestMissingAttributionFallsBackButZeroIsKnown(t *testing.T) {
	var per PerTest
	zero, err := parseF12(t, "mode: count\nexample/a/p.go:1.1,2.3 1 0\n")
	if err != nil {
		t.Fatal(err)
	}
	per.AddInPackage("example/a", "TestA", zero)
	tests, known := per.CoveringTestsForPackages("p.go", 1, 2, []string{"example/a"})
	if !known || len(tests) != 0 {
		t.Fatalf("instrumented zero-count block must be confidently uncovered, got %v known=%t", tests, known)
	}
	other, err := parseF12(t, "mode: count\nexample/a/other.go:1.1,2.3 1 1\n")
	if err != nil {
		t.Fatal(err)
	}
	per.AddInPackage("example/a", "TestB", other)
	if _, known := per.CoveringTestsForPackages("p.go", 1, 2, []string{"example/a"}); known {
		t.Fatal("missing file attribution for another test must force full-scope execution")
	}
}
