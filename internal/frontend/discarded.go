package frontend

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Discarded-result equivalence: the returnvalue operator's mutation of
// result i of a function is provably unobservable when every possible
// caller throws result i away (`_, m := oom(x)`, or a bare `oom(x)`).
//
// The proof is deliberately narrow, and every restriction exists to
// keep it from being wrong:
//
//   - The function must be an unexported package-level function, so
//     every possible caller is in its own package directory. That is
//     what turns a whole-module call graph into a bounded scan. Methods
//     are excluded (an unexported method can be called through an
//     interface), as are generic functions, `init`, and `main`.
//   - It must have unnamed results. With named results a deferred
//     closure can read the value after the return statement assigns it,
//     so "the caller discards it" would not mean "nothing observes it".
//   - Every unqualified identifier in the package that spells the
//     function's name must be the callee of a call that is either a
//     bare expression statement or the sole right-hand side of an
//     assignment with exactly one left-hand side per result, with a
//     blank identifier in position i. Any other mention -- a function
//     value, a `go` or `defer` call, an argument, a return, a shadowing
//     variable, a method or field of the same name -- disqualifies it,
//     which can only make the check more conservative, never less.
//   - At least one call site must exist, so a function that is simply
//     never called is not reported as "discarded".
//   - Every .go file in the directory with the same package name is
//     scanned, test files and files excluded by build constraints
//     included, because a call site that the current build would not
//     compile could still exist on another platform. If any of them
//     fails to parse, imports "C", or mentions the name in a
//     //export directive, or if any assembly file mentions the name,
//     nothing is claimed. A //go:linkname directive naming the function
//     anywhere in the module also blocks the claim, because another
//     package could use one to call an unexported function; the whole
//     module is read once, lazily, for that.
//   - The replaced expression itself must have no side effects and
//     cannot panic (pureResultExpr): replacing it would otherwise
//     remove a call or a nil dereference the caller can observe even
//     though it never sees the value.

type pkgCache struct {
	root         string
	byDir        map[string]*pkgInfo
	linknameText *string
}

func newPkgCache(root string) *pkgCache {
	return &pkgCache{root: root, byDir: map[string]*pkgInfo{}}
}

// linknames returns every //go:linkname line in the module's Go files,
// and false if the module could not be read completely.
func (c *pkgCache) linknames() (string, bool) {
	if c.linknameText != nil {
		return *c.linknameText, true
	}
	var b strings.Builder
	complete := true
	_ = filepath.WalkDir(c.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			complete = false
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			complete = false
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "go:linkname") {
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
		return nil
	})
	if !complete {
		return "", false
	}
	text := b.String()
	c.linknameText = &text
	return text, true
}

// pkgInfo is every parsed same-package file in one directory.
type pkgInfo struct {
	files []*ast.File
	asm   string
	bad   bool
	cgo   bool
}

func (c *pkgCache) load(dir, pkgName string) *pkgInfo {
	key := dir + "\x00" + pkgName
	if info, ok := c.byDir[key]; ok {
		return info
	}
	info := &pkgInfo{}
	c.byDir[key] = info
	entries, err := os.ReadDir(dir)
	if err != nil {
		info.bad = true
		return info
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		switch {
		case strings.HasSuffix(e.Name(), ".go"):
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				info.bad = true
				continue
			}
			if f.Name.Name != pkgName {
				continue
			}
			for _, imp := range f.Imports {
				if imp.Path.Value == `"C"` {
					info.cgo = true
				}
			}
			info.files = append(info.files, f)
		case strings.HasSuffix(e.Name(), ".s"):
			b, err := os.ReadFile(path)
			if err != nil {
				info.bad = true
				continue
			}
			info.asm += string(b) + "\n"
		}
	}
	return info
}

// eligibleDiscardDecl reports whether decl is the kind of function the
// proof can reason about at all.
func eligibleDiscardDecl(decl *ast.FuncDecl) bool {
	if decl == nil || decl.Recv != nil || decl.Body == nil || decl.Type.TypeParams != nil {
		return false
	}
	name := decl.Name.Name
	if name == "init" || name == "main" || name == "_" || ast.IsExported(name) {
		return false
	}
	if decl.Type.Results == nil || len(decl.Type.Results.List) == 0 {
		return false
	}
	for _, f := range decl.Type.Results.List {
		if len(f.Names) > 0 {
			return false
		}
	}
	return true
}

type callSite struct {
	all   bool
	blank map[int]bool
}

// resultDiscarded reports whether result idx (of n) of the package-level
// function name is discarded at every call site, and how many call
// sites there are.
func (info *pkgInfo) resultDiscarded(name string, n, idx int) (sites int, ok bool) {
	if info == nil || info.bad || info.cgo || strings.Contains(info.asm, name) {
		return 0, false
	}
	for _, f := range info.files {
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if (strings.HasPrefix(c.Text, "//export") || strings.HasPrefix(c.Text, "//go:linkname")) && strings.Contains(c.Text, name) {
					return 0, false
				}
			}
		}
		okFun := map[*ast.Ident]callSite{}
		calleeOf := func(call *ast.CallExpr) *ast.Ident {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
				return id
			}
			return nil
		}
		ast.Inspect(f, func(node ast.Node) bool {
			switch x := node.(type) {
			case *ast.ExprStmt:
				if call, ok := x.X.(*ast.CallExpr); ok {
					if id := calleeOf(call); id != nil {
						okFun[id] = callSite{all: true}
					}
				}
			case *ast.AssignStmt:
				if len(x.Rhs) != 1 || len(x.Lhs) != n {
					return true
				}
				call, ok := x.Rhs[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				if id := calleeOf(call); id != nil {
					blank := map[int]bool{}
					for i, l := range x.Lhs {
						if b, ok := l.(*ast.Ident); ok && b.Name == "_" {
							blank[i] = true
						}
					}
					okFun[id] = callSite{blank: blank}
				}
			}
			return true
		})
		declIdent := map[*ast.Ident]bool{}
		bad := false
		ast.Inspect(f, func(node ast.Node) bool {
			if bad {
				return false
			}
			switch x := node.(type) {
			case *ast.FuncDecl:
				if x.Recv == nil && x.Name.Name == name {
					declIdent[x.Name] = true
				}
			case *ast.Ident:
				if x.Name != name || declIdent[x] {
					return true
				}
				site, isCall := okFun[x]
				if !isCall || !(site.all || site.blank[idx]) {
					bad = true
					return false
				}
				sites++
			}
			return true
		})
		if bad {
			return 0, false
		}
	}
	return sites, sites > 0
}

// pureResultExpr reports whether evaluating e can neither have a side
// effect nor panic: identifiers, literals, and non-dividing, non-shifting
// arithmetic, ordered comparison and logic over them. Equality is
// excluded because comparing interface values can panic, and selectors,
// indexing, dereference and calls can all panic or have effects.
func pureResultExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return pureResultExpr(x.X)
	case *ast.Ident, *ast.BasicLit:
		return true
	case *ast.UnaryExpr:
		switch x.Op {
		case token.ADD, token.SUB, token.NOT, token.XOR:
			return pureResultExpr(x.X)
		}
	case *ast.BinaryExpr:
		switch x.Op {
		case token.ADD, token.SUB, token.MUL, token.AND, token.OR, token.XOR, token.AND_NOT,
			token.LSS, token.LEQ, token.GTR, token.GEQ, token.LAND, token.LOR:
			return pureResultExpr(x.X) && pureResultExpr(x.Y)
		}
	}
	return false
}

// discardReason returns the EquivalentReason for replacing result idx
// (of n) of decl, or "" when the proof does not apply.
func (c *pkgCache) discardReason(dir, pkgName string, decl *ast.FuncDecl, idx, n int) string {
	if !eligibleDiscardDecl(decl) {
		return ""
	}
	name := decl.Name.Name
	links, ok := c.linknames()
	if !ok || strings.Contains(links, name) {
		return ""
	}
	sites, ok := c.load(dir, pkgName).resultDiscarded(name, n, idx)
	if !ok {
		return ""
	}
	return fmt.Sprintf("result %d of unexported %s is discarded at all %d of its call sites in package %s and the replaced expression has no side effects and cannot panic, so no caller can observe the change",
		idx+1, name, sites, pkgName)
}
