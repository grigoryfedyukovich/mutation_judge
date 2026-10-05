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
	"strconv"
	"strings"

	"github.com/example/mutation-judge/internal/model"
)

const SemanticsVersion = "mutation-judge-operators/v10"

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
	// deadStoreLit maps a `:=` short variable declaration's own boolean
	// literal RHS (*ast.Ident, "true" or "false") to the human-readable
	// reason it's provably equivalent under boolean-literal mutation,
	// populated by the *ast.BlockStmt case below before ast.Inspect's
	// pre-order walk reaches that same nested node (that literal is a
	// descendant of the very declaration statement inside the block
	// being scanned) -- see detectDeadStoreLiteral's doc comment.
	deadStoreLit := map[*ast.Ident]string{}
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
	// to mutate any integer or string literal in this set -- see its
	// case below for why.
	loopSensitiveLit := map[*ast.BasicLit]bool{}
	// importPathLit marks an *ast.ImportSpec's own path string,
	// populated by the *ast.ImportSpec case below before ast.Inspect's
	// pre-order walk reaches that same nested literal. Not a
	// timeout-safety exclusion like the two above -- there's no hang
	// risk here -- but emptying an import path (`import ""`) is a
	// guaranteed compile failure on essentially every Go file, since
	// nearly every file imports something. Without this, the literal
	// operator's string-emptying mutation would spend a mutant on a
	// 100%-certain, zero-information INVALID verdict on every single
	// import in every file it ever ran against.
	importPathLit := map[*ast.BasicLit]bool{}
	// stmtNoDelete marks statements the statement operator must never
	// delete because the deletion could hang or is unsafe to reason
	// about: anything inside a `go` statement, and anything inside a
	// for loop whose termination the statement might influence (see
	// markLoopStatements). Populated by the *ast.ForStmt and
	// *ast.GoStmt cases before ast.Inspect's pre-order walk reaches
	// the nested statements.
	stmtNoDelete := map[ast.Stmt]bool{}
	// condNoNegate marks if statements the condition operator must
	// not negate because the negation could hang, recurse forever, or
	// starve a waiter (see the condition operator's comment). Populated
	// by the *ast.FuncDecl, *ast.FuncLit, *ast.ForStmt and
	// *ast.RangeStmt cases before the walk reaches the nested ifs.
	condNoNegate := map[*ast.IfStmt]bool{}
	// uses is only built when the statement operator is enabled.
	var uses *useIndex
	if opts.Operators["statement"] {
		uses = newUseIndex(f)
	}
	// deleteStatements offers each deletable statement in list to add.
	// Only statements that sit directly in a statement list are
	// considered, never a for/if/switch Init or Post clause.
	deleteStatements := func(list []ast.Stmt) {
		if !opts.Operators["statement"] {
			return
		}
		for _, st := range list {
			if stmtNoDelete[st] || !deletableStatement(st) || uses.orphans(st) {
				continue
			}
			text := clipStatement(source(src, fset, st.Pos(), st.End()))
			if call, ok := callOf(st); ok {
				add("statement", "MJ-STMT-DELETE-CALL", st.Pos(), st.End(), "",
					fmt.Sprintf("delete call statement %s", text),
					fmt.Sprintf("add a test that observes the effect of %s; if nothing observable depends on it, the call may be removable", clipStatement(source(src, fset, call.Pos(), call.End()))), "")
				continue
			}
			add("statement", "MJ-STMT-DELETE-ASSIGN", st.Pos(), st.End(), "",
				fmt.Sprintf("delete assignment %s", text),
				"add an assertion on the state this statement sets; a test that only checks the returned error will not notice it is gone", "")
		}
	}
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
					fmt.Sprintf("replace %s with %s", x.Name, repl), "exercise the branch controlled by this boolean constant", deadStoreLit[x])
			}
		case *ast.FuncDecl:
			if x.Body != nil && (containsGoto(x.Body) || selfRecursive(x)) {
				markIfsUnder(x.Body, condNoNegate)
			}
		case *ast.FuncLit:
			if containsGoto(x.Body) {
				markIfsUnder(x.Body, condNoNegate)
			}
		case *ast.IfStmt:
			if opts.Operators["condition"] && negatableCondition(x, opts, condNoNegate) {
				text := source(src, fset, x.Cond.Pos(), x.Cond.End())
				add("condition", "MJ-COND-NEGATE", x.Cond.Pos(), x.Cond.End(), "!("+text+")",
					fmt.Sprintf("negate condition %s", clipStatement(text)),
					"add a case where this condition is true and one where it is false, and assert the different outcome of each branch", "")
			}
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
		case *ast.BlockStmt:
			// Boolean-literal equivalent-mutant suppression: this only
			// ever populates deadStoreLit, never calls add() directly --
			// the *ast.Ident case above is what actually emits the
			// (possibly-marked) boolean-literal mutant once ast.Inspect's
			// pre-order walk reaches the nested literal node, which is a
			// descendant of this very block (see detectDeadStoreLiteral's
			// doc comment for exactly which pattern this requires).
			detectDeadStoreLiteral(x.List, src, fset, deadStoreLit)
			deleteStatements(x.List)
		case *ast.CommClause:
			deleteStatements(x.Body)
		case *ast.GoStmt:
			// Recorded unconditionally, like loopProgressStmt: whatever
			// runs on another goroutine may be what unblocks a waiter,
			// so deleting any of it risks an uninformative TIMEOUT.
			markAllStatements(x.Call, stmtNoDelete)
		case *ast.CaseClause:
			deleteStatements(x.Body)
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
			markLoopStatements(x, stmtNoDelete)
			markLoopIfs(x, condNoNegate)
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
			markRangeIfs(x, condNoNegate)
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
		case *ast.ImportSpec:
			if x.Path != nil {
				importPathLit[x.Path] = true
			}
		case *ast.BasicLit:
			if opts.Operators["literal"] && !loopSensitiveLit[x] {
				switch x.Kind {
				case token.INT:
					if inc, dec, ok := literalIntReplacements(x); ok {
						add("literal", "MJ-LITERAL-INC", x.Pos(), x.End(), inc,
							fmt.Sprintf("replace integer literal %s with %s", x.Value, inc),
							"add a small table-driven case that distinguishes the original constant from one larger", "")
						add("literal", "MJ-LITERAL-DEC", x.Pos(), x.End(), dec,
							fmt.Sprintf("replace integer literal %s with %s", x.Value, dec),
							"add a small table-driven case that distinguishes the original constant from one smaller", "")
					}
				case token.STRING:
					if !importPathLit[x] {
						if repl, ok := literalStringEmptyReplacement(x); ok {
							add("literal", "MJ-LITERAL-STRING-EMPTY", x.Pos(), x.End(), repl,
								fmt.Sprintf("replace string literal %s with %s", x.Value, repl),
								"add a test that distinguishes the original string content from an empty one", "")
						}
					}
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

// detectDeadStoreLiteral implements the boolean operator's second
// conservative equivalent-mutant suppression: a `:=` declaration whose
// literal initializer is provably overwritten before it can be read,
// so flipping true/false there is unobservable. It recognizes exactly
// two shapes, each requiring the overwrite to be the very next
// statement in the same statement list.
//
// Straight-line overwrite:
//
//	seen := false
//	seen = probe(x)
//
// Exhaustive if/else overwrite:
//
//	seen := false
//	if probe(x) {
//		seen = true
//	} else {
//		seen = false
//	}
//
// In both, the value of seen after that next statement was decided
// entirely by an assignment that never reads seen, and which
// assignment ran was decided by a condition that never reads seen
// either -- so the original and the mutant are in the same state the
// moment the next statement finishes, in any program state, not just
// the ones the current suite reaches.
//
// This is deliberately as narrow as detectGuardedComparison, for the
// same reason: every restriction below exists to rule out a way the
// proof could be wrong, not for simplicity. Anything not recognized --
// an else-if chain, a multi-statement branch, a read anywhere before
// the overwrite, a path that leaves the variable untouched, an
// overwrite that is not the very next statement -- is left as an
// ordinary mutant, generated and executed exactly as before. In
// particular this is NOT a general liveness analysis: a variable that
// is only overwritten on some paths (a loop that sets a flag when it
// finds something, say) keeps its initializer live on the other
// paths and is never matched.
//
// Restrictions:
//
//   - The declaration must be `:=` with exactly one identifier on the
//     left (not `_`) and a bare `true`/`false` identifier as the whole
//     right-hand side.
//   - The overwrite must be the immediately following statement of the
//     same statement list, so nothing can run, and no label or goto
//     target can intervene, between the declaration and it.
//   - An overwrite is a plain `v = <expr>` (never `:=`, which would
//     shadow, and never a compound assignment) with exactly one
//     variable on each side, where <expr> does not mention v by name
//     (see referencesIdent): `v = v || c` reads the very value the
//     proof needs to be unreadable.
//   - For the if/else shape, the if must have no init clause (it could
//     introduce or shadow a variable), its condition must not mention
//     v, and it must have a plain `else` block -- never an `else if`
//     chain and never a missing else, since a path that skips both
//     assignments would still hold the original literal. Both blocks
//     must be exactly one statement, an overwrite as defined above;
//     as with detectGuardedComparison's single-statement body there is
//     no attempt to look into longer branches, where an earlier
//     statement could read v first.
//
// The condition restriction is what makes the if/else proof hold. If
// the condition read v, the two runs could take different branches: with
// `seen := false; if seen { seen = true } else { seen = false }` the
// original ends false and a true-initialized mutant ends true.
func detectDeadStoreLiteral(list []ast.Stmt, src []byte, fset *token.FileSet, out map[*ast.Ident]string) {
	for i := 0; i+1 < len(list); i++ {
		decl, ok := list[i].(*ast.AssignStmt)
		if !ok || decl.Tok != token.DEFINE || len(decl.Lhs) != 1 || len(decl.Rhs) != 1 {
			continue
		}
		v, ok := decl.Lhs[0].(*ast.Ident)
		if !ok || v.Name == "_" {
			continue
		}
		lit, ok := decl.Rhs[0].(*ast.Ident)
		if !ok || (lit.Name != "true" && lit.Name != "false") {
			continue
		}
		switch next := list[i+1].(type) {
		case *ast.AssignStmt:
			if !isOverwrite(next, v.Name) {
				continue
			}
			out[lit] = fmt.Sprintf(
				"the next statement (line %d) reassigns %s without reading it, so this initializer's literal value can never be observed",
				fset.Position(next.Pos()).Line, v.Name,
			)
		case *ast.IfStmt:
			if next.Init != nil || referencesIdent(next.Cond, v.Name) {
				continue
			}
			elseBlock, ok := next.Else.(*ast.BlockStmt)
			if !ok {
				continue
			}
			if !isSoleOverwrite(next.Body, v.Name) || !isSoleOverwrite(elseBlock, v.Name) {
				continue
			}
			out[lit] = fmt.Sprintf(
				"the next statement (line %d, if %q) reassigns %s without reading it on every branch, so this initializer's literal value can never be observed",
				fset.Position(next.Pos()).Line, compact(source(src, fset, next.Cond.Pos(), next.Cond.End())), v.Name,
			)
		}
	}
}

// isOverwrite reports whether assign is a plain (non-shadowing,
// non-compound) single assignment `name = <expr>` whose right-hand
// side does not itself reference name -- see detectDeadStoreLiteral
// for why both restrictions matter.
func isOverwrite(assign *ast.AssignStmt, name string) bool {
	if assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return false
	}
	lhs, ok := assign.Lhs[0].(*ast.Ident)
	if !ok || lhs.Name != name {
		return false
	}
	return !referencesIdent(assign.Rhs[0], name)
}

// isSoleOverwrite reports whether block is exactly one statement, and
// that statement is an isOverwrite of name.
func isSoleOverwrite(block *ast.BlockStmt, name string) bool {
	if len(block.List) != 1 {
		return false
	}
	assign, ok := block.List[0].(*ast.AssignStmt)
	return ok && isOverwrite(assign, name)
}

// referencesIdent reports whether e contains an *ast.Ident named name
// anywhere in its subtree. Purely syntactic, like sameOperand: it does
// not need type information, only "does this expression mention this
// name at all", answered conservatively (matching by name, not by
// resolved identity) so a false negative here would require an
// entirely unrelated, differently-scoped variable that happens to
// share the name -- a case detectDeadStoreLiteral's single-statement,
// single-assignment scope gives no room for.
func referencesIdent(e ast.Expr, name string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// The statement operator deletes one bare statement. It is the only
// operator that can reproduce "the test covers this line but never
// asserts on what it does": a field assignment or a call whose effect
// no test observes survives deletion no matter how the line is
// covered. Every restriction below exists to keep a deletion from
// producing a guaranteed-uninformative verdict (INVALID or TIMEOUT)
// or from silently duplicating a proof the tool cannot yet make.
//
// Deletable statements:
//
//   - an assignment or ++/-- whose every target is a field selector,
//     index expression, or pointer dereference. Never a bare
//     identifier: stores to locals and named results are very often
//     dead stores (see detectDeadStoreLiteral), and deleting one would
//     spend a mutant on an equivalence this tool cannot decide.
//     `:=` is never deleted; it would orphan every later use.
//   - a call statement, except the exclusions in deletableCall.
//
// Never deleted: anything inside a `go` statement, anything inside a
// for loop whose termination it might influence (markLoopStatements),
// and any statement whose deletion would leave a local variable or an
// imported package with no remaining use (useIndex.orphans), which
// the compiler rejects.
func deletableStatement(st ast.Stmt) bool {
	switch x := st.(type) {
	case *ast.AssignStmt:
		if x.Tok == token.DEFINE {
			return false
		}
		for _, l := range x.Lhs {
			switch unwrapParen(l).(type) {
			case *ast.SelectorExpr, *ast.IndexExpr, *ast.StarExpr:
			default:
				return false
			}
		}
		return len(x.Lhs) > 0
	case *ast.IncDecStmt:
		switch unwrapParen(x.X).(type) {
		case *ast.SelectorExpr, *ast.IndexExpr, *ast.StarExpr:
			return true
		}
		return false
	case *ast.ExprStmt:
		call, ok := x.X.(*ast.CallExpr)
		return ok && deletableCall(call)
	}
	return false
}

func callOf(st ast.Stmt) (*ast.CallExpr, bool) {
	e, ok := st.(*ast.ExprStmt)
	if !ok {
		return nil, false
	}
	call, ok := e.X.(*ast.CallExpr)
	return call, ok
}

// unsafeCallNames are call names whose deletion tends to hang or end
// the run rather than change a result: lock/wait/signal primitives,
// closers, cancellation, and process/test termination. Deleting
// `mu.Unlock()` or `wg.Done()` produces a TIMEOUT, which is the
// uninformative verdict the operators avoid by construction.
var unsafeCallNames = map[string]bool{
	"Lock": true, "Unlock": true, "RLock": true, "RUnlock": true, "TryLock": true, "TryRLock": true,
	"Done": true, "Wait": true, "Signal": true, "Broadcast": true, "Acquire": true, "Release": true,
	"Close": true, "CloseSend": true, "CloseRead": true, "CloseWrite": true, "Shutdown": true,
	"Cancel": true, "cancel": true,
	"Exit": true, "Fatal": true, "Fatalf": true, "Fatalln": true, "Panic": true, "Panicf": true, "Panicln": true,
	"FailNow": true, "Skip": true, "Skipf": true, "SkipNow": true,
	"panic": true, "close": true, "recover": true, "print": true, "println": true,
}

// loggingCallNames are excluded for noise rather than safety: nobody
// asserts on log output, so every such deletion would just survive.
var loggingCallNames = map[string]bool{
	"Debug": true, "Debugf": true, "Info": true, "Infof": true, "Warn": true, "Warnf": true,
	"Log": true, "Logf": true, "Print": true, "Printf": true, "Println": true,
}

func deletableCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return !unsafeCallNames[fn.Name]
	case *ast.SelectorExpr:
		name := fn.Sel.Name
		if unsafeCallNames[name] || loggingCallNames[name] {
			return false
		}
		// wg.Add(1): deleting it panics on the matching Done -- a fast
		// but meaningless kill.
		if name == "Add" && len(call.Args) == 1 {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.INT {
				return false
			}
		}
		if base, ok := fn.X.(*ast.Ident); ok && (base.Name == "log" || base.Name == "slog") {
			return false
		}
		return true
	}
	return false
}

// markAllStatements marks every assignment, ++/--, and call statement
// under n as undeletable.
func markAllStatements(n ast.Node, out map[ast.Stmt]bool) {
	ast.Inspect(n, func(c ast.Node) bool {
		switch st := c.(type) {
		case *ast.AssignStmt:
			out[st] = true
		case *ast.IncDecStmt:
			out[st] = true
		case *ast.ExprStmt:
			out[st] = true
		}
		return true
	})
}

// markLoopStatements marks the statements in a for loop's body that
// must not be deleted because they may be what makes the loop end. In
// a loop with no post clause or no condition, that is everything:
// `for !done() { step() }` ends only because of something in the body,
// and nothing local says what. In a counted loop
// (`for i := 0; i < n; i++`) only a statement that mentions a name from
// the loop header is at risk. Range loops are bounded by their operand
// and are not marked.
func markLoopStatements(loop *ast.ForStmt, out map[ast.Stmt]bool) {
	if loop.Cond == nil || loop.Post == nil {
		markAllStatements(loop.Body, out)
		return
	}
	header := map[string]bool{}
	for _, n := range []ast.Node{loop.Init, loop.Cond, loop.Post} {
		if n == nil {
			continue
		}
		ast.Inspect(n, func(c ast.Node) bool {
			if id, ok := c.(*ast.Ident); ok {
				header[id.Name] = true
			}
			return true
		})
	}
	ast.Inspect(loop.Body, func(c ast.Node) bool {
		st, ok := c.(ast.Stmt)
		if !ok {
			return true
		}
		switch st.(type) {
		case *ast.AssignStmt, *ast.IncDecStmt, *ast.ExprStmt:
		default:
			return true
		}
		mentions := false
		ast.Inspect(st, func(x ast.Node) bool {
			if id, ok := x.(*ast.Ident); ok && header[id.Name] {
				mentions = true
			}
			return !mentions
		})
		if mentions {
			out[st] = true
		}
		return true
	})
}

func clipStatement(s string) string {
	s = compact(s)
	if r := []rune(s); len(r) > 60 {
		return string(r[:57]) + "..."
	}
	return s
}

// useIndex answers "would deleting this statement leave something
// unused?", because Go rejects an unused local variable and an unused
// import, and a mutant that fails to compile is a guaranteed
// zero-information INVALID. It counts read occurrences: the bare
// identifier on the left of an assignment, a ++/--, a range key or
// value, and a declaring name are writes, not reads, and do not keep a
// variable alive. Parameters are exempt from the compiler's check and
// are ignored; package-level variables are treated like locals, which
// is conservative (it may skip a legal deletion, never permit an
// illegal one). Package names are recognized as unresolved selector
// bases (`strings` in `strings.ToUpper`) without needing import
// aliases. Built on go/ast object resolution, which is deprecated but
// populated by the parse mode discovery uses; anything unresolved
// simply makes the check more conservative.
type useIndex struct {
	reads map[*ast.Object]int
	bases map[string]int
}

func newUseIndex(f *ast.File) *useIndex {
	u := &useIndex{reads: map[*ast.Object]int{}, bases: map[string]int{}}
	countUses(f, u.reads, u.bases)
	return u
}

func countUses(root ast.Node, reads map[*ast.Object]int, bases map[string]int) {
	write := map[*ast.Ident]bool{}
	mark := func(e ast.Expr) {
		if id, ok := unwrapParen(e).(*ast.Ident); ok {
			write[id] = true
		}
	}
	ast.Inspect(root, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				mark(l)
			}
		case *ast.IncDecStmt:
			mark(x.X)
		case *ast.RangeStmt:
			if x.Key != nil {
				mark(x.Key)
			}
			if x.Value != nil {
				mark(x.Value)
			}
		case *ast.ValueSpec:
			for _, name := range x.Names {
				write[name] = true
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Obj == nil {
				bases[id.Name]++
			}
		case *ast.Ident:
			if !write[x] && x.Obj != nil && x.Obj.Kind == ast.Var {
				reads[x.Obj]++
			}
		}
		return true
	})
}

// orphans reports whether deleting n would leave a variable or an
// imported package with no remaining read outside n.
func (u *useIndex) orphans(n ast.Node) bool {
	reads := map[*ast.Object]int{}
	bases := map[string]int{}
	countUses(n, reads, bases)
	for obj, c := range reads {
		if _, param := obj.Decl.(*ast.Field); param {
			continue
		}
		// Declared inside n itself (a function literal's own locals):
		// deleting n deletes the declaration along with every use.
		if d, ok := obj.Decl.(ast.Node); ok && d.Pos() >= n.Pos() && d.End() <= n.End() {
			continue
		}
		if u.reads[obj]-c < 1 {
			return true
		}
	}
	for name, c := range bases {
		if u.bases[name]-c < 1 {
			return true
		}
	}
	return false
}

// The condition operator replaces an if statement's condition c with
// !(c). It is the standard "was each branch actually distinguished?"
// check: a test suite that never makes the condition false (or never
// true) lets the negation survive. Only `if` and `else if` conditions
// are negated -- never a for loop's, which the loop operator already
// treats with termination-safety rules -- and every restriction below
// exists to keep a negation from producing a guaranteed-uninformative
// TIMEOUT or runaway recursion, or from duplicating another operator:
//
//   - not inside a `for` loop that has no condition or no post clause
//     (the loop may end only because of something the negation could
//     flip), nor, in a counted loop, an if that mentions a name from the
//     loop header; not inside a `range` loop when the if has a `break`
//     (over a channel, that break may be the only way out).
//   - not in a function containing `goto` (a goto loop's exit is an if)
//     or one that calls itself by name (negating a base case recurses
//     until the runtime kills the process; mutual recursion is not
//     detected).
//   - not when the if's condition or branches touch concurrency: a
//     channel send or receive, `select`, `go`, or a call excluded by
//     unsafeCallNames (Lock, Wait, Done, Close, ...): negating the
//     guard can starve a waiter forever.
//   - not a constant `true`/`false` condition.
//   - not a `!x` condition when the boolean operator is enabled, nor a
//     `==`/`!=` comparison when the relational operator is enabled:
//     those mutations produce the same program, so the second one
//     would be a duplicate execution.
func negatableCondition(x *ast.IfStmt, opts Options, noNegate map[*ast.IfStmt]bool) bool {
	if noNegate[x] || x.Cond == nil {
		return false
	}
	cond := unwrapParen(x.Cond)
	switch c := cond.(type) {
	case *ast.Ident:
		if c.Name == "true" || c.Name == "false" {
			return false
		}
	case *ast.UnaryExpr:
		if c.Op == token.NOT && opts.Operators["boolean"] {
			return false
		}
	case *ast.BinaryExpr:
		if (c.Op == token.EQL || c.Op == token.NEQ) && opts.Operators["relational"] {
			return false
		}
	}
	if touchesConcurrency(x.Cond) || touchesConcurrency(x.Body) || (x.Else != nil && touchesConcurrency(x.Else)) {
		return false
	}
	return true
}

// touchesConcurrency reports whether n contains a channel send or
// receive, a select, a go statement, or a call named in unsafeCallNames.
func touchesConcurrency(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(c ast.Node) bool {
		if found {
			return false
		}
		switch x := c.(type) {
		case *ast.SendStmt, *ast.SelectStmt, *ast.GoStmt:
			found = true
		case *ast.UnaryExpr:
			if x.Op == token.ARROW {
				found = true
			}
		case *ast.CallExpr:
			switch fn := x.Fun.(type) {
			case *ast.Ident:
				found = unsafeCallNames[fn.Name]
			case *ast.SelectorExpr:
				found = unsafeCallNames[fn.Sel.Name]
			}
		}
		return !found
	})
	return found
}

func containsGoto(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(c ast.Node) bool {
		if b, ok := c.(*ast.BranchStmt); ok && b.Tok == token.GOTO {
			found = true
		}
		return !found
	})
	return found
}

// selfRecursive reports whether fn's body calls fn by name (a plain
// call for a function, a selector call with the method's name for a
// method). Deliberately name-based and conservative.
func selfRecursive(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(c ast.Node) bool {
		call, ok := c.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			found = fn.Recv == nil && f.Name == fn.Name.Name
		case *ast.SelectorExpr:
			found = fn.Recv != nil && f.Sel.Name == fn.Name.Name
		}
		return !found
	})
	return found
}

func markIfsUnder(n ast.Node, out map[*ast.IfStmt]bool) {
	ast.Inspect(n, func(c ast.Node) bool {
		if st, ok := c.(*ast.IfStmt); ok {
			out[st] = true
		}
		return true
	})
}

// markLoopIfs mirrors markLoopStatements for if statements.
func markLoopIfs(loop *ast.ForStmt, out map[*ast.IfStmt]bool) {
	if loop.Cond == nil || loop.Post == nil {
		markIfsUnder(loop.Body, out)
		return
	}
	header := map[string]bool{}
	for _, n := range []ast.Node{loop.Init, loop.Cond, loop.Post} {
		if n == nil {
			continue
		}
		ast.Inspect(n, func(c ast.Node) bool {
			if id, ok := c.(*ast.Ident); ok {
				header[id.Name] = true
			}
			return true
		})
	}
	ast.Inspect(loop.Body, func(c ast.Node) bool {
		st, ok := c.(*ast.IfStmt)
		if !ok {
			return true
		}
		mentions := false
		ast.Inspect(st, func(x ast.Node) bool {
			if id, ok := x.(*ast.Ident); ok && header[id.Name] {
				mentions = true
			}
			return !mentions
		})
		if mentions {
			out[st] = true
		}
		return true
	})
}

// markRangeIfs marks the ifs in a range loop's body that contain a
// break: over a channel, that break may be the only way out.
func markRangeIfs(loop *ast.RangeStmt, out map[*ast.IfStmt]bool) {
	ast.Inspect(loop.Body, func(c ast.Node) bool {
		st, ok := c.(*ast.IfStmt)
		if !ok {
			return true
		}
		ast.Inspect(st, func(x ast.Node) bool {
			if b, ok := x.(*ast.BranchStmt); ok && b.Tok == token.BREAK {
				out[st] = true
			}
			return !out[st]
		})
		return true
	})
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

// literalStringEmptyReplacement returns `""` for a non-empty string
// literal, and reports false for one that's already empty (an empty
// string mutated to itself is a no-op the generic add() filter would
// reject anyway, but skipping it here means the whole mutant is never
// considered in the first place, rather than being built and then
// discarded). strconv.Unquote handles both interpreted (`"..."`) and
// raw (backtick) string syntax and their escape sequences correctly;
// go/ast's BasicLit.Value is the literal exactly as written, quotes
// included, so the content itself is never available without
// unquoting it first. The replacement is always the plain
// double-quoted empty string regardless of the original's quoting
// style -- always valid Go wherever a string literal is, and there is
// no meaningful "raw" form of an empty string to preserve.
func literalStringEmptyReplacement(lit *ast.BasicLit) (string, bool) {
	s, err := strconv.Unquote(lit.Value)
	if err != nil || s == "" {
		return "", false
	}
	return `""`, true
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
