package frontend

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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
//     nothing is claimed. A real //go:linkname directive naming this
//     exact package-qualified symbol anywhere in the module also blocks
//     the claim, because another package could use it to call an
//     unexported function; the whole module is scanned once, lazily.
//   - The replaced expression itself must have no side effects and
//     cannot panic (pureResultExpr): replacing it would otherwise
//     remove a call or a nil dereference the caller can observe even
//     though it never sees the value.

type pkgCache struct {
	root             string
	byDir            map[string]*pkgInfo
	linknameScanned  bool
	linknameComplete bool
	linknameDecls    []linknameDecl
}

func newPkgCache(root string) *pkgCache {
	return &pkgCache{root: root, byDir: map[string]*pkgInfo{}}
}

// linknameDecl identifies a real line-comment compiler directive. The local
// symbol is resolved relative to the declaring directory; the remote symbol
// (when supplied) is an import-path-qualified symbol.
type linknameDecl struct {
	dir    string
	local  string
	remote string
}

// linknames scans Go lexical comments rather than source lines: text resembling
// a directive inside a string, raw string or ordinary comment is not a directive.
// Lexical errors or unreadable files invalidate the whole scan, so equivalence
// remains unproved if we cannot account for the module's source.
func (c *pkgCache) linknames() ([]linknameDecl, bool) {
	if c.linknameScanned {
		return c.linknameDecls, c.linknameComplete
	}
	c.linknameScanned = true
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
		fset := token.NewFileSet()
		file := fset.AddFile(path, -1, len(data))
		var lexErrors int
		var scan scanner.Scanner
		scan.Init(file, data, func(token.Position, string) { lexErrors++ }, scanner.ScanComments)
		for {
			_, tok, literal := scan.Scan()
			if tok == token.EOF {
				break
			}
			const prefix = "//go:linkname"
			if tok != token.COMMENT || !strings.HasPrefix(literal, prefix) {
				continue
			}
			rest := strings.TrimPrefix(literal, prefix)
			if len(rest) == 0 || (rest[0] != ' ' && rest[0] != '\t') {
				continue // e.g. //go:linknamed is not a directive
			}
			fields := strings.Fields(rest)
			if len(fields) < 1 || len(fields) > 2 {
				complete = false // an unrecognized directive cannot prove safety
				continue
			}
			decl := linknameDecl{dir: filepath.Clean(filepath.Dir(path)), local: fields[0]}
			if len(fields) == 2 {
				decl.remote = fields[1]
			}
			c.linknameDecls = append(c.linknameDecls, decl)
		}
		if lexErrors > 0 {
			complete = false
		}
		return nil
	})
	c.linknameComplete = complete
	return c.linknameDecls, complete
}

// importPath resolves the package's symbol namespace using its nearest go.mod.
// Without a module declaration, an ambiguous same-named remote symbol must be
// treated as possibly referring to the candidate rather than assumed unrelated.
func (c *pkgCache) importPath(dir string) (string, bool) {
	root, err := filepath.Abs(c.root)
	if err != nil {
		return "", false
	}
	current, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		rel, err := filepath.Rel(root, current)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		data, err := os.ReadFile(filepath.Join(current, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 2 || fields[0] != "module" {
					continue
				}
				path := fields[1]
				if strings.HasPrefix(path, "\"") || strings.HasPrefix(path, "`") {
					path, err = strconv.Unquote(path)
					if err != nil {
						return "", false
					}
				}
				subdir, err := filepath.Rel(current, dir)
				if err != nil {
					return "", false
				}
				if subdir != "." {
					path += "/" + filepath.ToSlash(subdir)
				}
				return path, true
			}
			return "", false
		}
		if !os.IsNotExist(err) || current == root {
			return "", false
		}
		current = filepath.Dir(current)
	}
}

func (c *pkgCache) mayBeLinked(dir, name string) bool {
	decls, complete := c.linknames()
	if !complete {
		return true
	}
	qualified, known := c.importPath(dir)
	if known {
		qualified += "." + name
	}
	for _, decl := range decls {
		// A local directive can export or redirect the target's own symbol.
		if decl.dir == filepath.Clean(dir) && decl.local == name {
			return true
		}
		if decl.remote == "" {
			continue
		}
		if known {
			if decl.remote == qualified {
				return true
			}
		} else if i := strings.LastIndexByte(decl.remote, '.'); i >= 0 && decl.remote[i+1:] == name {
			return true // no module identity: fail closed on an ambiguous target
		}
	}
	return false
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
				if strings.HasPrefix(c.Text, "//export") && strings.Contains(c.Text, name) {
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
	if c.mayBeLinked(dir, name) {
		return ""
	}
	sites, ok := c.load(dir, pkgName).resultDiscarded(name, n, idx)
	if !ok {
		return ""
	}
	return fmt.Sprintf("result %d of unexported %s is discarded at all %d of its call sites in package %s and the replaced expression has no side effects and cannot panic, so no caller can observe the change",
		idx+1, name, sites, pkgName)
}
