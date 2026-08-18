package frontend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/example/mutation-judge/internal/model"
)

const SemanticsVersion = "mutation-judge-operators/v1"

type Options struct {
	Operators        map[string]bool
	IncludeGenerated bool
	ChangedLines     map[string]map[int]bool
}

func Discover(root string, files []string, opts Options) ([]model.Mutation, error) {
	var all []model.Mutation
	for _, rel := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if !opts.IncludeGenerated && isGenerated(src) {
			continue
		}
		ms, err := discoverFile(rel, src, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, ms...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Span.File != all[j].Span.File {
			return all[i].Span.File < all[j].Span.File
		}
		if all[i].Span.StartByte != all[j].Span.StartByte {
			return all[i].Span.StartByte < all[j].Span.StartByte
		}
		return all[i].ID < all[j].ID
	})
	seenIDs := make(map[string]bool, len(all))
	for _, mut := range all {
		if seenIDs[mut.ID] {
			return nil, fmt.Errorf("internal invariant: duplicate mutation ID %s", mut.ID)
		}
		seenIDs[mut.ID] = true
	}
	return all, nil
}

func discoverFile(rel string, src []byte, opts Options) ([]model.Mutation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%s: parse error: %w; run gofmt or fix the reported syntax before mutation analysis", rel, err)
	}
	lineIndex := newLineIndex(src)
	var out []model.Mutation
	add := func(op, rule string, start, end token.Pos, replacement, description, suggestion string) {
		sp := span(fset, rel, start, end)
		if !overlapsChanged(sp, opts.ChangedLines) {
			return
		}
		if sp.StartByte < 0 || sp.EndByte > len(src) || sp.StartByte >= sp.EndByte {
			return
		}
		original := string(src[sp.StartByte:sp.EndByte])
		id := mutationID(rel, sp.StartByte, op, rule, original, replacement)
		out = append(out, model.Mutation{
			ID: id, Operator: op, RuleID: rule, Span: sp,
			Original: original, Replacement: replacement,
			Description: description, Suggestion: suggestion,
			Diff: unifiedDiff(rel, src, lineIndex, sp.StartByte, sp.EndByte, replacement),
		})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			if opts.Operators["boundary"] {
				if repl, ok := boundaryReplacement(x.Op); ok {
					add("boundary", "MJ-BOUNDARY", x.OpPos, x.OpPos+token.Pos(len(x.Op.String())), repl,
						fmt.Sprintf("replace comparison %s with %s", x.Op, repl), boundarySuggestion(x, src, fset))
				}
			}
			if opts.Operators["boolean"] && (x.Op == token.LAND || x.Op == token.LOR) {
				left := source(src, fset, x.X.Pos(), x.X.End())
				right := source(src, fset, x.Y.Pos(), x.Y.End())
				connector := x.Op.String()
				add("boolean", "MJ-BOOL-DROP-RIGHT", x.Pos(), x.End(), "("+left+")",
					fmt.Sprintf("delete the right operand of %s", connector),
					booleanDeletionSuggestion(x.Op, compact(left), compact(right)))
				add("boolean", "MJ-BOOL-DROP-LEFT", x.Pos(), x.End(), "("+right+")",
					fmt.Sprintf("delete the left operand of %s", connector),
					booleanDeletionSuggestion(x.Op, compact(right), compact(left)))
			}
			if opts.Operators["arithmetic"] {
				if repl, ok := arithmeticReplacement(x.Op); ok {
					add("arithmetic", "MJ-ARITHMETIC", x.OpPos, x.OpPos+token.Pos(len(x.Op.String())), repl,
						fmt.Sprintf("replace arithmetic operator %s with %s", x.Op, repl),
						"add a small table-driven case that distinguishes the original arithmetic result from the mutant")
				}
			}
		case *ast.UnaryExpr:
			if opts.Operators["boolean"] && x.Op == token.NOT {
				repl := "(" + source(src, fset, x.X.Pos(), x.X.End()) + ")"
				add("boolean", "MJ-BOOL-DROP-NOT", x.Pos(), x.End(), repl,
					"delete boolean negation", "add paired true/false cases that make the negation observable")
			}
		case *ast.Ident:
			if opts.Operators["boolean"] && (x.Name == "true" || x.Name == "false") {
				repl := "true"
				if x.Name == "true" {
					repl = "false"
				}
				add("boolean", "MJ-BOOL-LITERAL", x.Pos(), x.End(), repl,
					fmt.Sprintf("replace %s with %s", x.Name, repl), "exercise the branch controlled by this boolean constant")
			}
		}
		return true
	})
	return out, nil
}

func booleanDeletionSuggestion(op token.Token, kept, dropped string) string {
	switch op {
	case token.LAND:
		return fmt.Sprintf("add a case where %s is true and %s is false, then assert the conjunction remains false", kept, dropped)
	case token.LOR:
		return fmt.Sprintf("add a case where %s is false and %s is true, then assert the disjunction remains true", kept, dropped)
	default:
		return "add a case that makes the deleted boolean operand determine the result"
	}
}

func boundaryReplacement(op token.Token) (string, bool) {
	switch op {
	case token.LSS:
		return "<=", true
	case token.LEQ:
		return "<", true
	case token.GTR:
		return ">=", true
	case token.GEQ:
		return ">", true
	default:
		return "", false
	}
}

func arithmeticReplacement(op token.Token) (string, bool) {
	switch op {
	case token.ADD:
		return "-", true
	case token.SUB:
		return "+", true
	case token.MUL:
		return "/", true
	case token.QUO:
		return "*", true
	default:
		return "", false
	}
}

func boundarySuggestion(x *ast.BinaryExpr, src []byte, fset *token.FileSet) string {
	left := compact(source(src, fset, x.X.Pos(), x.X.End()))
	right := compact(source(src, fset, x.Y.Pos(), x.Y.End()))
	return fmt.Sprintf("add a boundary case where %s equals %s and assert the original branch behavior", left, right)
}

func source(src []byte, fset *token.FileSet, start, end token.Pos) string {
	a := fset.PositionFor(start, false).Offset
	b := fset.PositionFor(end, false).Offset
	if a < 0 || b < a || b > len(src) {
		return "<expression>"
	}
	return string(src[a:b])
}

func span(fset *token.FileSet, file string, start, end token.Pos) model.Span {
	a := fset.PositionFor(start, false)
	b := fset.PositionFor(end, false)
	return model.Span{File: filepath.ToSlash(file), StartByte: a.Offset, EndByte: b.Offset, StartLine: a.Line, StartCol: a.Column, EndLine: b.Line, EndCol: b.Column}
}

func overlapsChanged(sp model.Span, changed map[string]map[int]bool) bool {
	if changed == nil {
		return true
	}
	lines, ok := changed[filepath.ToSlash(sp.File)]
	if !ok {
		return false
	}
	for l := sp.StartLine; l <= sp.EndLine; l++ {
		if lines[l] {
			return true
		}
	}
	return false
}

func mutationID(file string, offset int, op, rule, original, replacement string) string {
	// Candidate strings are tiny and subprocess execution dominates runtime.
	// A truncated cryptographic digest gives stable persisted IDs with a clear
	// collision story; this is not used as a security boundary.
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s\x00%s", file, offset, op, rule, original, replacement)))
	return "M-" + hex.EncodeToString(h[:6])
}

func isGenerated(src []byte) bool {
	head := src
	if len(head) > 2048 {
		head = head[:2048]
	}
	return bytes.Contains(head, []byte("Code generated")) && bytes.Contains(head, []byte("DO NOT EDIT"))
}

func compact(s string) string { return strings.Join(strings.Fields(s), " ") }

type lineOffsets []int

func newLineIndex(src []byte) lineOffsets {
	offsets := lineOffsets{0}
	for i, b := range src {
		if b == '\n' && i+1 < len(src) {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

func (l lineOffsets) containing(offset int) int {
	i := sort.Search(len(l), func(i int) bool { return l[i] > offset })
	if i == 0 {
		return 0
	}
	return i - 1
}

func unifiedDiff(file string, src []byte, lines lineOffsets, start, end int, replacement string) string {
	startLine := lines.containing(start)
	endOffset := end
	if endOffset > start {
		endOffset--
	}
	endLine := lines.containing(endOffset)
	lineStart := lines[startLine]
	lineEnd := len(src)
	if endLine+1 < len(lines) {
		lineEnd = lines[endLine+1] - 1
	}
	oldBlock := string(src[lineStart:lineEnd])
	newBlock := string(src[lineStart:start]) + replacement + string(src[end:lineEnd])
	line := startLine + 1
	oldLines := strings.Split(oldBlock, "\n")
	newLines := strings.Split(newBlock, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", file, file, line, len(oldLines), line, len(newLines))
	for _, text := range oldLines {
		fmt.Fprintf(&b, "-%s\n", text)
	}
	for _, text := range newLines {
		fmt.Fprintf(&b, "+%s\n", text)
	}
	return b.String()
}
