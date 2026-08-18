package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func ModuleRoot(cwd string) (string, error) {
	cmd := exec.Command("go", "env", "GOMOD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD failed: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull || gomod == "/dev/null" {
		return "", fmt.Errorf("no Go module found from %s", cwd)
	}
	return filepath.Dir(gomod), nil
}

type Package struct {
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	ImportPath string
	Error      *struct{ Err string }
}

func ListPackages(cwd string, patterns []string) ([]Package, error) {
	args := append([]string{"list", "-json", "-e"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list failed: %w", err)
	}
	return decodePackages(out)
}

func SourceFiles(root string, pkgs []Package) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, p := range pkgs {
		if p.Error != nil && p.Error.Err != "" {
			return nil, fmt.Errorf("package %s: %s", p.ImportPath, p.Error.Err)
		}
		for _, name := range append(append([]string{}, p.GoFiles...), p.CgoFiles...) {
			abs := filepath.Join(p.Dir, name)
			rel, err := filepath.Rel(root, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("source file %s is outside module root", abs)
			}
			rel = filepath.ToSlash(rel)
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func CopyModule(root, cacheDir string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "mutation-judge-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	cacheAbs := cacheDir
	if !filepath.IsAbs(cacheAbs) {
		cacheAbs = filepath.Join(root, cacheDir)
	}
	cacheAbs, err = filepath.Abs(cacheAbs)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".mutation-judge" || filepath.Clean(path) == cacheAbs {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(tmp, rel), 0o755)
		}
		dst := filepath.Join(tmp, rel)
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, dst, info.Mode())
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp, cleanup, nil
}

func Apply(root string, rel string, start, end int, replacement string) (func() error, error) {
	path, err := secureExistingPath(root, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to mutate symlinked source file %s", rel)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if start < 0 || end <= start || end > len(original) {
		return nil, fmt.Errorf("invalid mutation span %d:%d for %s (%d bytes)", start, end, rel, len(original))
	}
	mutated := make([]byte, 0, len(original)-(end-start)+len(replacement))
	mutated = append(mutated, original[:start]...)
	mutated = append(mutated, replacement...)
	mutated = append(mutated, original[end:]...)
	if err := writeFileAtomic(path, mutated, info.Mode().Perm()); err != nil {
		return nil, err
	}
	return func() error { return writeFileAtomic(path, original, info.Mode().Perm()) }, nil
}

func secureExistingPath(root, rel string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	inside, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("mutation path escapes sandbox root: %s", rel)
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return "", err
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("mutation path resolves outside sandbox root: %s", rel)
	}
	return pathAbs, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	return writeFileAtomicWithRename(path, data, mode, os.Rename)
}

func writeFileAtomicWithRename(path string, data []byte, mode os.FileMode, rename func(string, string) error) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mutation-judge-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	defer cleanup()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return rename(tmpName, path)
}

func Digest(root string) (string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".mutation-judge" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".go") || name == "go.mod" || name == "go.sum" || name == "go.work" || name == "go.work.sum" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, p := range files {
		rel, _ := filepath.Rel(root, p)
		_, _ = io.WriteString(h, filepath.ToSlash(rel))
		_, _ = h.Write([]byte{0})
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(out, in)
	closeErr := out.Close()
	if cpErr != nil {
		return cpErr
	}
	return closeErr
}
