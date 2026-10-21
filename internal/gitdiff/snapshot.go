package gitdiff

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/example/mutation-judge/internal/proc"
	"github.com/example/mutation-judge/internal/workspace"
)

// SnapshotBase pins both the comparison commit and index before source copying.
// Diff and untracked-file selection subsequently read only a materialized copy
// of the input snapshot, never the mutable host worktree.
type SnapshotBase struct {
	gitDir, repoRoot, prefix, revision, indexPath string
	indexHash                                     [32]byte
	parentMetadata                                map[string][]byte
	temp                                          string
}

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := proc.Command(ctx, "git", args...)
	cmd.Dir = cwd
	out, err := proc.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CaptureSnapshotBase must run before CopyModule. The index and resolved
// revision are frozen, and later verified unchanged during acquisition.
func CaptureSnapshotBase(ctx context.Context, moduleRoot, base string) (*SnapshotBase, error) {
	top, err := gitOutput(ctx, moduleRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	top, err = filepath.Abs(top)
	if err != nil {
		return nil, err
	}
	moduleRoot, err = filepath.Abs(moduleRoot)
	if err != nil {
		return nil, err
	}
	prefix, err := filepath.Rel(top, moduleRoot)
	if err != nil || prefix == ".." || strings.HasPrefix(prefix, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("module %s is not within Git worktree %s", moduleRoot, top)
	}
	gitDir, err := gitOutput(ctx, moduleRoot, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	rev, err := gitOutput(ctx, moduleRoot, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return nil, err
	}
	indexPath, err := gitOutput(ctx, moduleRoot, "rev-parse", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(moduleRoot, indexPath)
	}
	indexPath, err = filepath.Abs(indexPath)
	if err != nil {
		return nil, err
	}
	index, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("capture Git index: %w", err)
	}
	temp, err := os.MkdirTemp("", "mutation-judge-git-snapshot-*")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(temp, "index"), index, 0600); err != nil {
		os.RemoveAll(temp)
		return nil, err
	}
	// A module can live beneath the Git repository root. Freeze its parent
	// ignore/attribute files, including absence, before source copying.
	metadata := make(map[string][]byte)
	parent := top
	for parent != moduleRoot {
		for _, name := range []string{".gitignore", ".gitattributes"} {
			path := filepath.Join(parent, name)
			rel, relErr := filepath.Rel(top, path)
			if relErr != nil {
				os.RemoveAll(temp)
				return nil, relErr
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil && !os.IsNotExist(readErr) {
				os.RemoveAll(temp)
				return nil, fmt.Errorf("capture ancestor Git metadata %s: %w", path, readErr)
			}
			metadata[rel] = data // nil records absence
		}
		remainder, _ := filepath.Rel(parent, moduleRoot)
		parent = filepath.Join(parent, strings.Split(remainder, string(filepath.Separator))[0])
	}

	return &SnapshotBase{gitDir: gitDir, repoRoot: top, prefix: prefix, revision: rev, indexPath: indexPath, indexHash: sha256.Sum256(index), parentMetadata: metadata, temp: temp}, nil
}

func (s *SnapshotBase) Close() {
	if s != nil {
		_ = os.RemoveAll(s.temp)
	}
}

// VerifyIndex prevents mixing a pre-copy index with a post-copy working tree
// when staging changes concurrently with preparation.
func (s *SnapshotBase) VerifyIndex() error {
	b, err := os.ReadFile(s.indexPath)
	if err != nil {
		return fmt.Errorf("verify Git index: %w", err)
	}
	if sha256.Sum256(b) != s.indexHash {
		return fmt.Errorf("Git index changed during snapshot acquisition; retry analysis")
	}
	for rel, prior := range s.parentMetadata {
		current, err := os.ReadFile(filepath.Join(s.repoRoot, rel))
		if os.IsNotExist(err) && prior == nil {
			continue
		}
		if err != nil || (prior == nil) || !bytes.Equal(current, prior) {
			return fmt.Errorf("ancestor Git metadata %s changed during snapshot acquisition", rel)
		}
	}
	return nil
}

// ChangedLinesFromSnapshotContext obtains both tracked diffs and untracked
// paths using an independent Git worktree populated from the frozen module.
func (s *SnapshotBase) ChangedLinesFromSnapshotContext(ctx context.Context, snapshot string) (map[string]map[int]bool, error) {
	container, err := os.MkdirTemp(s.temp, "tree-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(container)
	workroot := filepath.Join(container, "wt")
	dst := filepath.Join(workroot, s.prefix)
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return nil, err
	}
	// This is a temporary copy of already frozen bytes. The original snapshot
	// remains the sole source for Go discovery and execution.
	cp, cleanup, err := workspace.CopyModule(snapshot, "")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if err := os.Rename(cp, dst); err != nil {
		return nil, err
	}
	for rel, data := range s.parentMetadata {
		if data == nil {
			continue
		}
		// Metadata under the module itself is already in the frozen tree.
		if rel == filepath.Join(s.prefix, ".gitignore") || rel == filepath.Join(s.prefix, ".gitattributes") {
			continue
		}
		target := filepath.Join(workroot, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			return nil, err
		}
	}
	env := append(os.Environ(), "GIT_DIR="+s.gitDir, "GIT_WORK_TREE="+workroot, "GIT_INDEX_FILE="+filepath.Join(s.temp, "index"))
	run := func(args ...string) ([]byte, error) {
		cmd := proc.Command(ctx, "git", args...)
		cmd.Dir = dst
		cmd.Env = env
		out, err := proc.CombinedOutput(cmd)
		if err != nil {
			return nil, fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}
	diffArgs := []string{"-c", "core.quotepath=false", "diff", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", "--unified=0", "--no-ext-diff", "--no-textconv"}
	if s.prefix != "." {
		diffArgs = append(diffArgs, "--relative="+filepath.ToSlash(s.prefix))
	}
	diffArgs = append(diffArgs, s.revision, "--", ":(glob)**/*.go")
	diff, err := run(diffArgs...)
	if err != nil {
		return nil, err
	}
	lines, err := Parse(diff)
	if err != nil {
		return nil, fmt.Errorf("parse snapshot Git diff: %w", err)
	}
	paths, err := run("ls-files", "--others", "--exclude-standard", "-z", "--", ":(glob)**/*.go")
	if err != nil {
		return nil, err
	}
	for _, item := range bytes.Split(paths, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		path := string(item)
		if !strings.HasSuffix(path, ".go") || filepath.IsAbs(path) || path == "." || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") || strings.HasPrefix(path, "./") {
			return nil, fmt.Errorf("invalid untracked Go path %q", path)
		}
		lines[path] = map[int]bool{0: true}
	}
	return lines, nil
}
