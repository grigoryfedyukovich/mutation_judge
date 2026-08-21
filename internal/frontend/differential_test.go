package frontend

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestDifferentialMutatedSource is a differential fixture suite: for each
// case it applies a discovered mutant's Span/Replacement directly to the
// original source bytes and compares the *entire reconstructed file*
// against a hand-verified expected string, rather than checking counts,
// descriptions, or hunk headers as the other tests in this package do.
// This is the only place that actually proves what mutation-judge writes
// into a person's source tree, byte for byte -- including the
// precedence/parenthesization behavior for nested boolean expressions,
// which is easy to get subtly wrong and easy for a test suite that only
// checks descriptions to miss.
//
// Every "want" string below was verified against this package's actual
// Discover output before being written here (not hand-calculated), so a
// mismatch means a real behavior change, not a stale fixture.
func TestDifferentialMutatedSource(t *testing.T) {
	type tc struct {
		name     string
		source   string
		ruleID   string
		original string // disambiguates when multiple mutants share a rule ID
		want     string
	}
	cases := []tc{
		{
			name:     "boundary operator token only, surrounding structure untouched",
			source:   "package p\n\nfunc f(n int) bool { return n > 0 }\n",
			ruleID:   "MJ-BOUNDARY",
			original: ">",
			want:     "package p\n\nfunc f(n int) bool { return n >= 0 }\n",
		},
		{
			name:     "arithmetic operator token only",
			source:   "package p\n\nfunc f(a, b int) int { return a + b }\n",
			ruleID:   "MJ-ARITHMETIC",
			original: "+",
			want:     "package p\n\nfunc f(a, b int) int { return a - b }\n",
		},
		{
			name:   "&& has higher precedence than ||: mutating the inner && subexpression must not touch the outer || c",
			source: "package p\n\nfunc f(a, b, c bool) bool {\n\treturn a && b || c\n}\n",
			ruleID: "MJ-BOOL-DROP-RIGHT", original: "a && b",
			want: "package p\n\nfunc f(a, b, c bool) bool {\n\treturn (a) || c\n}\n",
		},
		{
			name:   "mutating the whole a && b || c expression parenthesizes the compound left operand correctly",
			source: "package p\n\nfunc f(a, b, c bool) bool {\n\treturn a && b || c\n}\n",
			ruleID: "MJ-BOOL-DROP-RIGHT", original: "a && b || c",
			want: "package p\n\nfunc f(a, b, c bool) bool {\n\treturn (a && b)\n}\n",
		},
		{
			name:   "left-associative && chain: mutating the middle subexpression leaves both ends of the chain untouched",
			source: "package p\n\nfunc f(a, b, c, d bool) bool {\n\treturn a && b && c && d\n}\n",
			ruleID: "MJ-BOOL-DROP-RIGHT", original: "a && b",
			want: "package p\n\nfunc f(a, b, c, d bool) bool {\n\treturn (a) && c && d\n}\n",
		},
		{
			name:   "left-associative && chain: mutating the largest left-nested subexpression leaves only the last operand outside",
			source: "package p\n\nfunc f(a, b, c, d bool) bool {\n\treturn a && b && c && d\n}\n",
			ruleID: "MJ-BOOL-DROP-RIGHT", original: "a && b && c",
			want: "package p\n\nfunc f(a, b, c, d bool) bool {\n\treturn (a && b) && d\n}\n",
		},
		{
			name:   "NOT of an already-parenthesized operand double-wraps rather than losing or duplicating source text incorrectly",
			source: "package p\n\nfunc f(a, b, c bool) bool {\n\treturn !(a && b) || c\n}\n",
			ruleID: "MJ-BOOL-DROP-NOT", original: "!(a && b)",
			want: "package p\n\nfunc f(a, b, c bool) bool {\n\treturn ((a && b)) || c\n}\n",
		},
		{
			name:   "boundary comparison nested inside a boolean tree: only the operator token changes, && / || structure is untouched",
			source: "package p\n\nfunc f(x, y int, c bool) bool {\n\treturn x > 0 && y < 10 || c\n}\n",
			ruleID: "MJ-BOUNDARY", original: ">",
			want: "package p\n\nfunc f(x, y int, c bool) bool {\n\treturn x >= 0 && y < 10 || c\n}\n",
		},
		{
			name:   "second boundary comparison in the same tree is independently addressable",
			source: "package p\n\nfunc f(x, y int, c bool) bool {\n\treturn x > 0 && y < 10 || c\n}\n",
			ruleID: "MJ-BOUNDARY", original: "<",
			want: "package p\n\nfunc f(x, y int, c bool) bool {\n\treturn x > 0 && y <= 10 || c\n}\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := t.TempDir()
			if err := os.WriteFile(filepath.Join(d, "p.go"), []byte(c.source), 0o644); err != nil {
				t.Fatal(err)
			}
			ms, err := Discover(d, []string{"p.go"}, Options{Operators: map[string]bool{"boundary": true, "boolean": true, "arithmetic": true}})
			if err != nil {
				t.Fatal(err)
			}
			var match *struct {
				startByte, endByte int
				replacement        string
			}
			var seen []string
			for _, m := range ms {
				seen = append(seen, fmt.Sprintf("%s original=%q", m.RuleID, m.Original))
				if m.RuleID == c.ruleID && m.Original == c.original {
					match = &struct {
						startByte, endByte int
						replacement        string
					}{m.Span.StartByte, m.Span.EndByte, m.Replacement}
				}
			}
			if match == nil {
				t.Fatalf("no mutant with rule %s and original %q found among %d discovered: %v", c.ruleID, c.original, len(ms), seen)
			}
			full := []byte(c.source)
			got := string(full[:match.startByte]) + match.replacement + string(full[match.endByte:])
			if got != c.want {
				t.Fatalf("mutated source mismatch\n--- want ---\n%s\n--- got ---\n%s", c.want, got)
			}
			if _, err := parser.ParseFile(token.NewFileSet(), "p.go", got, 0); err != nil {
				t.Fatalf("reconstructed mutated source does not parse as valid Go: %v\n%s", err, got)
			}
		})
	}
}
