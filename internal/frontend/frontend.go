package frontend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/example/mutation-judge/internal/model"
)

const SemanticsVersion = "mutation-judge-operators/v6"

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
	add := func(op, rule string, start, end token.Pos, replacement, description, suggestion, equivalentReason string) {
		sp := span(fset, rel, start, end)
		if !overlapsChanged(sp, opts.ChangedLines) {
			return
		}
		if sp.StartByte < 0 || sp.EndByte > len(src) || sp.StartByte >= sp.EndByte {
			return
		}
		original := string(src[sp.StartByte:sp.EndByte])
		if original == replacement {
			return
		}
		id := mutationID(rel, sp.StartByte, op, rule, original, replacement)
		out = append(out, model.Mutation{
			ID: id, Operator: op, RuleID: rule, Span: sp,
			Original: original, Replacement: replacement,
			Description: description, Suggestion: suggestion,
			Diff:             unifiedDiff(rel, src, lineIndex, sp.StartByte, sp.EndByte, replacement),
			EquivalentReason: equivalentReason,
		})
	}
	// equivalentGuard maps a comparison's *ast.BinaryExpr node to the
	// human-readable reason it's provably equivalent under boundary
	// mutation, populated by the *ast.IfStmt case below before
	// ast.Inspect's pre-order walk reaches that same nested node --
	// see detectGuardedComparison's doc comment for exactly what
	// pattern this requires.
	equivalentGuard := map[*ast.BinaryExpr]string{}
	// loopProgressStmt marks exactly the statements that are a for
	// loop's own Post clause (`for init; cond; post { ... }`),
	// populated by the *ast.ForStmt case below before ast.Inspect's
	// pre-order walk reaches that same nested statement. The
	// assignment operator refuses to mutate any statement in this set
	// -- see its case below for why.
	loopProgressStmt := map[ast.Stmt]bool{}
	// loopCondExpr marks every *ast.BinaryExpr appearing anywhere
	// within a for statement's own condition -- including nested
	// inside a compound `&&`/`||` condition, not just a bare top-level
	// comparison -- populated by the *ast.ForStmt case below before
	// ast.Inspect's pre-order walk reaches those same nested nodes.
	// The relational operator refuses to mutate any expression in
	// this set -- see its case below for why.
	loopCondExpr := map[*ast.BinaryExpr]bool{}
	// loopSensitiveLit marks every *ast.BasicLit appearing anywhere
	// within a for statement's own condition or post clause, for the
	// same reason and by the same nested-walk technique as
	// loopCondExpr and loopProgressStmt. The literal operator refuses
	// to mutate any integer literal in this set -- see its case below
	// for why.
	loopSensitiveLit := map[*ast.BasicLit]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			if opts.Operators["boundary"] {
				if repl, ok := boundaryReplacement(x.Op); ok {
					add("boundary", "MJ-BOUNDARY", x.OpPos, x.OpPos+token.Pos(len(x.Op.String())), repl,
						fmt.Sprintf("replace comparison %s with %s", x.Op, repl), boundarySuggestion(x, src, fset),
						equivalentGuard[x])
				}
			}
			if opts.Operators["boolean"] && (x.Op == token.LAND || x.Op == token.LOR) {
				left := source(src, fset, x.X.Pos(), x.X.End())
				right := source(src, fset, x.Y.Pos(), x.Y.End())
				connector := x.Op.String()
				add("boolean", "MJ-BOOL-DROP-RIGHT", x.Pos(), x.End(), "("+left+")",
					fmt.Sprintf("delete the right operand of %s", connector),
					booleanDeletionSuggestion(x.Op, compact(left), compact(right)), "")
				add("boolean", "MJ-BOOL-DROP-LEFT", x.Pos(), x.End(), "("+right+")",
					fmt.Sprintf("delete the left operand of %s", connector),
					booleanDeletionSuggestion(x.Op, compact(right), compact(left)), "")
			}
			if opts.Operators["arithmetic"] {
				if repl, ok := arithmeticReplacement(x.Op); ok {
					add("arithmetic", "MJ-ARITHMETIC", x.OpPos, x.OpPos+token.Pos(len(x.Op.String())), repl,
						fmt.Sprintf("replace arithmetic operator %s with %s", x.Op, repl),
						"add a small table-driven case that distinguishes the original arithmetic result from the mutant", "")
				}
			}
			if opts.Operators["relational"] && !loopCondExpr[x] {
				if repl, ok := relationalReplacement(x.Op); ok {
					add("relational", "MJ-RELATIONAL", x.OpPos, x.OpPos+token.Pos(len(x.Op.String())), repl.String(),
						fmt.Sprintf("replace %s with %s", x.Op, repl),
						"add a small table-driven case that distinguishes the original equality result from the mutant", "")
				}
			}
		case *ast.UnaryExpr:
			if opts.Operators["boolean"] && x.Op == token.NOT {
				repl := "(" + source(src, fset, x.X.Pos(), x.X.End()) + ")"
				add("boolean", "MJ-BOOL-DROP-NOT", x.Pos(), x.End(), repl,
					"delete boolean negation", "add paired true/false cases that make the negation observable", "")
			}
		case *ast.Ident:
			if opts.Operators["boolean"] && (x.Name == "true" || x.Name == "false") {
				repl := "true"
				if x.Name == "true" {
					repl = "false"
				}
				add("boolean", "MJ-BOOL-LITERAL", x.Pos(), x.End(), repl,
					fmt.Sprintf("replace %s with %s", x.Name, repl), "exercise the branch controlled by this boolean constant", "")
			}
		case *ast.IfStmt:
			if opts.Operators["errorreturn"] {
				if checked, ok := notNilOperand(x.Cond); ok {
					checkedIdent, ok := checked.(*ast.Ident)
					if ok {
						for _, stmt := range x.Body.List {
							ret, ok := stmt.(*ast.ReturnStmt)
							if !ok || len(ret.Results) == 0 {
								continue
							}
							last := ret.Results[len(ret.Results)-1]
							lastIdent, ok := last.(*ast.Ident)
							if !ok || lastIdent.Name != checkedIdent.Name {
								continue
							}
							add("errorreturn", "MJ-ERR-SWALLOW", last.Pos(), last.End(), "nil",
								fmt.Sprintf("swallow the checked value: replace returned %s with nil", checkedIdent.Name),
								fmt.Sprintf("add a test that triggers this branch and asserts the propagated %s is actually non-nil, not just that the call fails", checkedIdent.Name), "")
						}
					}
				}
			}
			// Boundary equivalent-mutant suppression: this only ever
			// populates equivalentGuard, never calls add() directly --
			// the *ast.BinaryExpr case above is what actually emits the
			// (possibly-marked) boundary mutant once ast.Inspect's
			// pre-order walk reaches the nested comparison node.
			detectGuardedComparison(x, src, fset, equivalentGuard)
		case *ast.CaseClause:
			if opts.Operators["switch"] && len(x.Body) > 0 {
				label := "default"
				if len(x.List) > 0 {
					label = compact(source(src, fset, x.List[0].Pos(), x.List[len(x.List)-1].End()))
				}
				add("switch", "MJ-SWITCH-DROP-CASE", x.Pos(), x.End(), "",
					fmt.Sprintf("delete case %s", label),
					fmt.Sprintf("add a test that exercises case %s and would fail if that case were missing", label), "")
			}
		case *ast.ForStmt:
			if x.Post != nil {
				// Recorded unconditionally, regardless of whether the
				// loop operator itself is enabled: this is the
				// assignment operator's exclusion, not the loop
				// operator's, and needs to be in place before the walk
				// reaches x.Post either way.
				loopProgressStmt[x.Post] = true
			}
			if x.Cond != nil {
				// Recorded unconditionally, same reasoning as above,
				// but for the relational and literal operators'
				// exclusions instead: flipping == to != (or back)
				// anywhere in a loop's own termination test --
				// including nested inside a compound && / ||
				// condition -- can turn a terminating loop into one
				// that never terminates, unlike a boundary (</<=/>/>=)
				// swap, which only ever shifts a monotonic threshold
				// by one step and so cannot change whether the
				// comparison eventually flips. A literal shift is
				// ordinarily just as safe as a boundary shift for the
				// same reason, but this pass has no way to rule out an
				// exotic non-monotonic loop whose termination depends
				// on an exact value (`for i != 10 { i *= 2 }`), so
				// literal mutation is excluded from a loop's own
				// condition too, matching relational's scope exactly
				// rather than trying to draw a finer, harder-to-verify
				// line. ast.Inspect here is a small, separate walk
				// over just this one condition subtree, not the whole
				// file.
				ast.Inspect(x.Cond, func(n ast.Node) bool {
					switch v := n.(type) {
					case *ast.BinaryExpr:
						loopCondExpr[v] = true
					case *ast.BasicLit:
						loopSensitiveLit[v] = true
					}
					return true
				})
			}
			if x.Post != nil {
				// A for loop's post clause is where a literal
				// mutation is genuinely, structurally dangerous: `for
				// i := n; i > 0; i -= 1 { ... }` mutated to `i -= 0`
				// removes the loop's only progress toward termination
				// outright, the same "runs forever" failure mode
				// loopProgressStmt already exists to prevent at the
				// operator level (a += / -= / ++ / -- swap) -- this
				// closes the same hole reached through a literal
				// operand instead of the assignment operator itself.
				ast.Inspect(x.Post, func(n ast.Node) bool {
					if lit, ok := n.(*ast.BasicLit); ok {
						loopSensitiveLit[lit] = true
					}
					return true
				})
			}
			if opts.Operators["loop"] {
				if x.Cond != nil {
					add("loop", "MJ-LOOP-COND-FALSE", x.Cond.Pos(), x.Cond.End(), "false",
						"force the loop condition false (loop body never executes)",
						"add a test that depends on the loop body actually running at least once", "")
				} else if len(x.Body.List) > 0 {
					first := x.Body.List[0]
					add("loop", "MJ-LOOP-BREAK-FIRST", first.Pos(), first.End(), "break",
						"insert an immediate break (loop body never executes)",
						"add a test that depends on the loop body actually running", "")
				}
			}
		case *ast.RangeStmt:
			if opts.Operators["loop"] && len(x.Body.List) > 0 {
				first := x.Body.List[0]
				add("loop", "MJ-LOOP-BREAK-FIRST", first.Pos(), first.End(), "break",
					"insert an immediate break (loop body never executes)",
					"add a test that depends on the loop body actually running", "")
			}
		case *ast.IncDecStmt:
			if opts.Operators["assignment"] && !loopProgressStmt[x] {
				repl, ok := incDecReplacement(x.Tok)
				if ok {
					add("assignment", "MJ-ASSIGN-INCDEC", x.TokPos, x.TokPos+token.Pos(len(x.Tok.String())), repl.String(),
						fmt.Sprintf("replace %s with %s", x.Tok, repl),
						"add a test that distinguishes the original increment/decrement direction from its opposite", "")
				}
			}
		case *ast.AssignStmt:
			if opts.Operators["assignment"] && !loopProgressStmt[x] {
				repl, ok := assignmentOpReplacement(x.Tok)
				if ok {
					add("assignment", "MJ-ASSIGN-OP", x.TokPos, x.TokPos+token.Pos(len(x.Tok.String())), repl.String(),
						fmt.Sprintf("replace compound assignment %s with %s", x.Tok, repl),
						"add a small table-driven case that distinguishes the original assignment result from the mutant", "")
				}
			}
		case *ast.BasicLit:
			if opts.Operators["literal"] && x.Kind == token.INT && !loopSensitiveLit[x] {
				if inc, dec, ok := literalIntReplacements(x); ok {
					add("literal", "MJ-LITERAL-INC", x.Pos(), x.End(), inc,
						fmt.Sprintf("replace integer literal %s with %s", x.Value, inc),
						"add a small table-driven case that distinguishes the original constant from one larger", "")
					add("literal", "MJ-LITERAL-DEC", x.Pos(), x.End(), dec,
						fmt.Sprintf("replace integer literal %s with %s", x.Value, dec),
						"add a small table-driven case that distinguishes the original constant from one smaller", "")
				}
			}
		case *ast.SelectStmt:
			if opts.Operators["channel"] && len(x.Body.List) > 1 {
				// Deleting the last remaining comm clause leaves
				// `select {}`, which blocks forever -- a guaranteed
				// TIMEOUT rather than a useful KILLED/SURVIVED. The
				// loop operator already refuses mutations that would
				// run forever; skip the same way when there is only
				// one case (handled by the len > 1 guard).
				for _, stmt := range x.Body.List {
					c, ok := stmt.(*ast.CommClause)
					if !ok || len(c.Body) == 0 {
						continue
					}
					label := "default"
					if c.Comm != nil {
						label = compact(source(src, fset, c.Comm.Pos(), c.Comm.End()))
					}
					add("channel", "MJ-CHAN-SELECT-DROP-CASE", c.Pos(), c.End(), "",
						fmt.Sprintf("delete select case %s", label),
						fmt.Sprintf("add a test that exercises the %s communication and would fail if that case were missing", label), "")
				}
			}
		case *ast.CallExpr:
			if opts.Operators["channel"] {
				if fn, ok := x.Fun.(*ast.Ident); ok && fn.Name == "make" && len(x.Args) == 2 {
					if _, ok := x.Args[0].(*ast.ChanType); ok {
						capArg := x.Args[1]
						capSrc := compact(source(src, fset, capArg.Pos(), capArg.End()))
						if capSrc != "0" {
							add("channel", "MJ-CHAN-UNBUFFER", capArg.Pos(), capArg.End(), "0",
								fmt.Sprintf("replace channel capacity %s with 0 (make it unbuffered)", capSrc),
								"add a test that depends on the channel being buffered, e.g. a non-blocking send before any receiver is ready", "")
						}
					}
				}
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

// detectGuardedComparison implements the boundary operator's
// conservative equivalent-mutant suppression: the "guarded sort
// comparisons" pattern documented in docs/evaluation.md, e.g.
//
//	if a.Field != b.Field {
//		return a.Field < b.Field
//	}
//
// Inside that if-body, a.Field != b.Field is already known true, so
// a.Field < b.Field and a.Field <= b.Field (or > / >=) are the exact
// same relation there -- equality, the one case strict and
// non-strict comparison disagree on, is unreachable. Mutating the
// comparison's operator between strict and non-strict is therefore
// unobservable by any test, in any state, not just untested by the
// current suite: this is a real proof, not a heuristic guess.
//
// This match is deliberately narrow -- every restriction below exists
// specifically to rule out a way the "proof" could be wrong, not for
// simplicity:
//
//   - x.Init must be nil: an init statement can introduce or shadow a
//     variable the comparison relies on, which the guard's guarantee
//     would then not actually be about.
//   - The if-body must be exactly one statement, a bare return of the
//     comparison: this rules out any intervening statement that could
//     reassign an operand between the guard and the comparison. There
//     is deliberately no attempt to look further into a multi-statement
//     body for "the" dominated comparison.
//   - The guard must be a literal X != Y (token.NEQ) directly as the
//     if's condition (parens unwrapped): no &&/||, no !(X == Y), no
//     other logically-equivalent-but-differently-shaped form. Matching
//     more shapes here would mean trusting a wider surface of pattern
//     recognition instead of one exact, checked case.
//   - Both operands must be side-effect-free (see
//     isSideEffectFreeOperand) -- no function/method calls, no channel
//     receives -- and the comparison's two operands must be exactly the
//     guard's two operands (see sameOperand), in either order. Without
//     this, "the same expression" could silently mean "two calls that
//     happen to look identical but can return different values".
//
// Any case this doesn't recognize is left as an ordinary mutant --
// generated and executed exactly as before. A missed equivalent mutant
// is a survivor a human can review; a wrongly suppressed one is a
// false claim of certainty printed in a report, which is the failure
// mode this function exists to avoid, not just to reduce.
func detectGuardedComparison(x *ast.IfStmt, src []byte, fset *token.FileSet, out map[*ast.BinaryExpr]string) {
	if x.Init != nil {
		return
	}
	guard, ok := unwrapParen(x.Cond).(*ast.BinaryExpr)
	if !ok || guard.Op != token.NEQ {
		return
	}
	if len(x.Body.List) != 1 {
		return
	}
	ret, ok := x.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return
	}
	cmp, ok := ret.Results[0].(*ast.BinaryExpr)
	if !ok {
		return
	}
	switch cmp.Op {
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
	default:
		return
	}
	if !isSideEffectFreeOperand(guard.X) || !isSideEffectFreeOperand(guard.Y) {
		return
	}
	matches := (sameOperand(guard.X, cmp.X) && sameOperand(guard.Y, cmp.Y)) ||
		(sameOperand(guard.X, cmp.Y) && sameOperand(guard.Y, cmp.X))
	if !matches {
		return
	}
	out[cmp] = fmt.Sprintf(
		"dominated by the enclosing guard %q (line %d): that check already establishes the two operands are unequal, so this comparison's strict/non-strict boundary can never be observed",
		compact(source(src, fset, guard.Pos(), guard.End())), fset.Position(x.Pos()).Line,
	)
}

// isSideEffectFreeOperand reports whether e is built purely from
// identifiers, field selectors, index expressions, pointer
// dereferences, parenthesization, and basic literals -- nothing that
// could have a side effect or read a different value on a second,
// textually adjacent evaluation (no function/method calls, no channel
// receives). This is what makes reasoning about "the same expression,
// evaluated twice, reads the same value" safe without any actual
// data-flow analysis: given detectGuardedComparison's single-statement
// body requirement, nothing can execute between the guard's evaluation
// and the comparison's, so a side-effect-free, textually identical
// expression is guaranteed to still read the same underlying storage.
func isSideEffectFreeOperand(e ast.Expr) bool {
	switch x := unwrapParen(e).(type) {
	case *ast.Ident, *ast.BasicLit:
		return true
	case *ast.SelectorExpr:
		return isSideEffectFreeOperand(x.X)
	case *ast.IndexExpr:
		return isSideEffectFreeOperand(x.X) && isSideEffectFreeOperand(x.Index)
	case *ast.StarExpr:
		return isSideEffectFreeOperand(x.X)
	default:
		return false
	}
}

// sameOperand reports whether a and b are the exact same expression,
// textually: same identifier names, same selector/index/star
// structure all the way down. It is purely syntactic -- it has no
// type information and does not need any, because it only needs to
// answer "is this literally the same read, written the same way": if
// it isn't textually identical, the two expressions might read
// different storage (a different field, a different index), so this
// conservatively answers no rather than guessing.
func sameOperand(a, b ast.Expr) bool {
	a, b = unwrapParen(a), unwrapParen(b)
	switch x := a.(type) {
	case *ast.Ident:
		y, ok := b.(*ast.Ident)
		return ok && x.Name == y.Name
	case *ast.SelectorExpr:
		y, ok := b.(*ast.SelectorExpr)
		return ok && x.Sel.Name == y.Sel.Name && sameOperand(x.X, y.X)
	case *ast.IndexExpr:
		y, ok := b.(*ast.IndexExpr)
		return ok && sameOperand(x.X, y.X) && sameOperand(x.Index, y.Index)
	case *ast.StarExpr:
		y, ok := b.(*ast.StarExpr)
		return ok && sameOperand(x.X, y.X)
	case *ast.BasicLit:
		y, ok := b.(*ast.BasicLit)
		return ok && x.Kind == y.Kind && x.Value == y.Value
	default:
		return false
	}
}

func unwrapParen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
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

// assignmentOpReplacement mirrors arithmeticReplacement's four
// operators (+/-, */÷) one level up, at the compound-assignment
// statement (`x += y`) instead of the binary expression (`x + y`).
// Deliberately excluded, matching arithmeticReplacement's own
// restraint: %=, the bitwise/shift compound assignments (&=, |=, ^=,
// &^=, <<=, >>=), and plain `=`/`:=` (not an operator to mutate at
// all -- there is no sibling to swap it with).
func assignmentOpReplacement(op token.Token) (token.Token, bool) {
	switch op {
	case token.ADD_ASSIGN:
		return token.SUB_ASSIGN, true
	case token.SUB_ASSIGN:
		return token.ADD_ASSIGN, true
	case token.MUL_ASSIGN:
		return token.QUO_ASSIGN, true
	case token.QUO_ASSIGN:
		return token.MUL_ASSIGN, true
	default:
		return token.ILLEGAL, false
	}
}

// incDecReplacement swaps ++ for -- and back. The one thing that
// makes this different from every other pointwise-swap operator in
// this file is that the single most common home for an IncDecStmt is
// a for loop's own post clause (`for i := 0; i < n; i++`), where
// flipping the direction doesn't produce a fast KILLED or SURVIVED --
// it produces an infinite loop for essentially any ordinary counting
// loop, the same "runs forever" failure mode the loop and channel
// operators already refuse to generate. See loopProgressStmt in
// discoverFile and its check at this function's call site; this
// function itself has no way to know where its argument came from, so
// the exclusion has to happen there, not here.
func incDecReplacement(op token.Token) (token.Token, bool) {
	switch op {
	case token.INC:
		return token.DEC, true
	case token.DEC:
		return token.INC, true
	default:
		return token.ILLEGAL, false
	}
}

// relationalReplacement swaps == for != and back -- Relational
// Operator Replacement restricted to equality, the one relational
// pair boundary's </<=/>/>= swap does not already cover. Unlike a
// boundary swap, which only ever shifts a monotonic threshold by one
// step and so cannot change whether a loop's own termination test
// eventually flips, an equality/inequality swap inverts the test's
// polarity outright: a `for x != target { ... }` loop mutated to `for
// x == target` can run zero times or run forever, depending entirely
// on x's actual trajectory, which this pass has no way to know. See
// loopCondExpr in discoverFile and its check at this function's call
// site for the resulting exclusion; this function itself has no way
// to know where its argument came from, so the exclusion has to
// happen there, not here.
func relationalReplacement(op token.Token) (token.Token, bool) {
	switch op {
	case token.EQL:
		return token.NEQ, true
	case token.NEQ:
		return token.EQL, true
	default:
		return token.ILLEGAL, false
	}
}

// literalIntReplacements returns the decimal text of lit's value plus
// one and minus one. It uses go/constant rather than strconv directly
// because go/ast's BasicLit.Value is the literal exactly as written --
// hex (`0x2A`), octal (`0o17`/`017`), binary (`0b101`), and
// underscore-separated (`1_000_000`) forms are all valid Go source
// go/constant already knows how to parse; reimplementing that parsing
// with strconv would either reject valid literals or silently misread
// them. The replacement text is always plain decimal regardless of
// the original literal's base -- simpler and just as correct, since
// Go accepts any integer literal in any base wherever one is valid;
// only the mutant's diff looks different from the original's style.
// A literal too large to fit in an int64 (valid Go for a uint64
// constant, or one only ever used as an untyped constant) is skipped
// rather than risk misrepresenting it, and so is a literal sitting
// exactly on the int64 boundary, where n+1 or n-1 would silently wrap
// around in Go's own int64 arithmetic (confirmed with a standalone
// experiment: int64(math.MaxInt64)+1 wraps to math.MinInt64) --
// astronomically unlikely to matter for a real literal, but wrong is
// wrong, and skipping the whole literal at that exact edge case costs
// nothing worth having.
func literalIntReplacements(lit *ast.BasicLit) (inc, dec string, ok bool) {
	v := constant.MakeFromLiteral(lit.Value, lit.Kind, 0)
	if v.Kind() != constant.Int {
		return "", "", false
	}
	n, exact := constant.Int64Val(v)
	if !exact || n == math.MaxInt64 || n == math.MinInt64 {
		return "", "", false
	}
	return fmt.Sprintf("%d", n+1), fmt.Sprintf("%d", n-1), true
}

// notNilOperand returns the non-nil side of a top-level `X != nil` (or
// `nil != X`) comparison -- the shape the errorreturn operator targets
// for the canonical `if err != nil { return err }` pattern. This pass
// has no type information, so it cannot confirm the checked value is
// actually an error; anything else guarded the same way (a nil-checked
// pointer, map, or slice returned directly by the same statement) is
// matched too, which is intentional -- an early return whose value gets
// silently swallowed is a meaningful mutant regardless of the checked
// value's exact type.
func notNilOperand(cond ast.Expr) (ast.Expr, bool) {
	be, ok := unwrapParen(cond).(*ast.BinaryExpr)
	if !ok || be.Op != token.NEQ {
		return nil, false
	}
	// Unwrap parens on each operand too, not just on cond itself: `if
	// (err) != nil` and `if err != (nil)` are exactly as common in
	// practice as `if (err != nil)` (gofmt does not touch either
	// shape), and both the nil check below and the value returned to
	// the caller need the bare identifier underneath, not a
	// *ast.ParenExpr wrapping it -- the caller does its own
	// checked.(*ast.Ident) type assertion and has no unwrapping of its
	// own.
	x, y := unwrapParen(be.X), unwrapParen(be.Y)
	xNil, yNil := isNilIdent(x), isNilIdent(y)
	switch {
	case yNil && !xNil:
		return x, true
	case xNil && !yNil:
		return y, true
	default:
		return nil, false
	}
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
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
