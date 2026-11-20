package frontend

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
)

// boolFlipIndex records source offsets whose identifier is a predeclared
// boolean constant and whose flipped spelling is the other predeclared
// boolean constant at that same position. Offsets are byte offsets in the
// file, so they match a separately parsed copy of the same source.
type boolFlipIndex struct {
	ok     bool
	byFile map[string]map[int]struct{}
}

// failingImporter refuses every import. Resolving true and false only
// needs this package's own scopes: a missing dependency must not drop a
// real predeclared constant, and a failed import still installs a locally
// renamed import binding (import true "fmt") in the file scope.
type failingImporter struct{}

func (failingImporter) Import(path string) (*types.Package, error) {
	return nil, fmt.Errorf("import %q not loaded", path)
}

// predeclaredBoolFlip reports whether id is a use of universe true or
// false whose replacement spelling also denotes the other universe
// constant. Package-level declarations in any same-package file
// (including generated files and internal tests), import names, type
// parameters, parameters, and local declarations all count as shadows.
// An unreadable or unparsable sibling makes the answer false: a missed
// literal is safer than a false EQUIVALENT proof.
func predeclaredBoolFlip(opts Options, rel, pkgName string, fset *token.FileSet, id *ast.Ident) bool {
	if opts.pkgs == nil || pkgName == "" || id == nil || fset == nil {
		return false
	}
	file := filepath.Join(opts.root, filepath.FromSlash(rel))
	dir := filepath.Dir(file)
	return opts.pkgs.boolFlipAt(dir, pkgName, file, fset.Position(id.Pos()).Offset)
}

func (c *pkgCache) boolFlipAt(dir, pkgName, file string, offset int) bool {
	if c == nil {
		return false
	}
	if c.boolFlips == nil {
		c.boolFlips = map[string]*boolFlipIndex{}
	}
	key := dir + "\x00" + pkgName
	idx, ok := c.boolFlips[key]
	if !ok {
		idx = buildBoolFlipIndex(c.load(dir, pkgName))
		c.boolFlips[key] = idx
	}
	if !idx.ok {
		return false
	}
	_, yes := idx.byFile[filepath.Clean(file)][offset]
	return yes
}

func buildBoolFlipIndex(info *pkgInfo) *boolFlipIndex {
	idx := &boolFlipIndex{byFile: map[string]map[int]struct{}{}}
	if info == nil || info.bad || info.fset == nil || len(info.files) == 0 {
		idx.ok = info != nil && !info.bad
		return idx
	}
	tinfo := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: failingImporter{}, Error: func(error) {}}
	pkg, _ := conf.Check(info.files[0].Name.Name, info.fset, info.files, tinfo)
	if pkg == nil {
		return idx
	}
	idx.ok = true
	trueObj := types.Universe.Lookup("true")
	falseObj := types.Universe.Lookup("false")
	for id, obj := range tinfo.Uses {
		var otherName string
		var other types.Object
		switch id.Name {
		case "true":
			if obj != trueObj {
				continue
			}
			otherName, other = "false", falseObj
		case "false":
			if obj != falseObj {
				continue
			}
			otherName, other = "true", trueObj
		default:
			continue
		}
		sc := pkg.Scope().Innermost(id.Pos())
		if sc == nil {
			continue
		}
		_, repl := sc.LookupParent(otherName, id.Pos())
		if repl != other {
			continue
		}
		pos := info.fset.Position(id.Pos())
		file := filepath.Clean(pos.Filename)
		set := idx.byFile[file]
		if set == nil {
			set = map[int]struct{}{}
			idx.byFile[file] = set
		}
		set[pos.Offset] = struct{}{}
	}
	return idx
}
