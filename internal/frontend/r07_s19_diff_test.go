package frontend

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// legacyUnifiedDiff is the pre-R07-S19 implementation, retained as an
// independent byte-for-byte reference for the optimized patch writer.
func legacyUnifiedDiff(file string, src []byte, lines lineOffsets, start, end int, replacement string) string {
	startLine := lines.containing(start)
	endOffset := end
	if endOffset > start {
		endOffset--
	}
	endLine := lines.containing(endOffset)
	lineStart := lines[startLine]
	lineEnd := len(src)
	if endLine+1 < len(lines) {
		lineEnd = lines[endLine+1]
	}
	oldBlock := src[lineStart:lineEnd]
	newBlock := make([]byte, 0, len(oldBlock)-(end-start)+len(replacement))
	newBlock = append(newBlock, src[lineStart:start]...)
	newBlock = append(newBlock, replacement...)
	newBlock = append(newBlock, src[end:lineEnd]...)
	ctxStartLine := startLine - 3
	if ctxStartLine < 0 {
		ctxStartLine = 0
	}
	ctxEndLine := endLine + 4
	if ctxEndLine > len(lines) {
		ctxEndLine = len(lines)
	}
	ctxStart := lines[ctxStartLine]
	ctxEnd := len(src)
	if ctxEndLine < len(lines) {
		ctxEnd = lines[ctxEndLine]
	}
	prefixLines := diffLines(src[ctxStart:lineStart])
	oldLines := diffLines(oldBlock)
	newLines := diffLines(newBlock)
	suffixLines := diffLines(src[lineEnd:ctxEnd])
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n",
		diffPath("a/", file), diffPath("b/", file), ctxStartLine+1,
		len(prefixLines)+len(oldLines)+len(suffixLines), ctxStartLine+1,
		len(prefixLines)+len(newLines)+len(suffixLines))
	for _, part := range []struct {
		prefix byte
		lines  [][]byte
	}{
		{' ', prefixLines}, {'-', oldLines}, {'+', newLines}, {' ', suffixLines},
	} {
		for _, line := range part.lines {
			b.WriteByte(part.prefix)
			b.Write(line)
			if line[len(line)-1] != '\n' {
				b.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	return b.String()
}

func TestR07S19UnifiedDiffEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(729331))
	files := []string{"p.go", "sub/quote\".go", "new\nline.go", "café.go", "back\\slash.go"}
	blocks := []string{"package p\nfunc F(x int) bool { return x > 0 }\n", "first\nsecond\nthird\n", "one\r\ntwo\r\nthree\r\n", "a\nb\nc", "x", "a\n\n\nlast\n", "a\n", "abc\r\n\r\nzz"}
	replacements := []string{"=", ">=", "", "\n", "hello\nworld", "\r\n", "α"}
	for _, file := range files {
		for _, block := range blocks {
			for rep := 0; rep < 40; rep++ {
				src := []byte(block)
				start := rng.Intn(len(src))
				end := start + 1 + rng.Intn(len(src)-start)
				replacement := replacements[rng.Intn(len(replacements))]
				idx := newLineIndex(src)
				want := legacyUnifiedDiff(file, src, idx, start, end, replacement)
				got := unifiedDiff(file, src, idx, start, end, replacement)
				if got != want {
					t.Fatalf("patch mismatch file=%q source=%q span=%d:%d replacement=%q\nwant:\n%s\ngot:\n%s", file, block, start, end, replacement, want, got)
				}
			}
		}
	}
}

func BenchmarkR07S19UnifiedDiff(b *testing.B) {
	src := []byte("package p\nfunc F(x int) bool { return x > 0 }\nfunc G(y int) bool { return y >= 2 }\n")
	idx := newLineIndex(src)
	start := strings.Index(string(src), "> 0")
	for _, tc := range []struct {
		name string
		fn   func(string, []byte, lineOffsets, int, int, string) string
	}{
		{"before", legacyUnifiedDiff},
		{"after", unifiedDiff},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = tc.fn("p.go", src, idx, start, start+1, ">=")
			}
		})
	}
}
