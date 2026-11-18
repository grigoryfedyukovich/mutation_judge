package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

type copyKind uint8

const (
	copyDirectory copyKind = iota
	copySymlink
	copyRegular
)

type copyEntry struct {
	rel    string
	kind   copyKind
	mode   os.FileMode
	target string
}

// CopyPlan records the exact entries of a completed private snapshot.
// Construct it only after all preparation-time changes (notably go.mod
// replacement rewriting) have finished. The source tree must not be
// modified until all copies finish. No writable file data is shared:
// every execution still materializes independent files, via CoW if safe.
// A plan is immutable and may be copied concurrently by multiple workers.
type CopyPlan struct {
	root    string
	entries []copyEntry
}

func NewCopyPlan(ctx context.Context, root, cacheDir string) (*CopyPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	p := &CopyPlan{root: abs}
	err = sandboxEntries(abs, cacheDir, func(path, rel string, d fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := copyEntry{rel: rel}
		if d.IsDir() {
			e.kind = copyDirectory
		} else if d.Type()&os.ModeSymlink != 0 {
			e.kind = copySymlink
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			e.target = target
		} else {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported sandbox entry type %s: %s", info.Mode().Type(), rel)
			}
			e.kind = copyRegular
			e.mode = info.Mode()
		}
		p.entries = append(p.entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (p *CopyPlan) Copy(ctx context.Context) (string, func(), error) {
	if p == nil {
		return "", nil, fmt.Errorf("nil snapshot copy plan")
	}
	workers := 1
	if raw := os.Getenv("MUTATION_JUDGE_COPY_WORKERS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 8 {
			return "", nil, fmt.Errorf("MUTATION_JUDGE_COPY_WORKERS must be an integer from 1 through 8, got %q", raw)
		}
		workers = n
	}
	return copyModuleEntries(ctx, p.root, workers, func(emit func(copyEntry) error) error {
		for _, entry := range p.entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emit(entry); err != nil {
				return err
			}
		}
		return nil
	})
}
