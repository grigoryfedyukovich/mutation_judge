package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/example/mutation-judge/internal/proc"
)

const localReplacementsDir = ".mutation-judge-local-replacements"

// IsolateLocalReplacements rewrites ONLY snapshot/go.mod, not the user's
// go.mod. Absolute replacements inside the main module are redirected into
// the snapshot; external replacements are materialized beneath the snapshot
// so subsequent CopyModule calls make an independent writable copy for every
// baseline, profiling, enumeration, and mutant execution.
//
// The Go toolchain ignores replace directives in dependency go.mod files; the
// main module's replacements are therefore the authoritative closure. We
// intentionally keep result caching disabled for local replacements until the
// entire resolved module graph is represented in cache identity.
func IsolateLocalReplacements(ctx context.Context, hostRoot, snapshot, cacheDir string) (bool, error) {
	cmd := proc.Command(ctx, "go", "mod", "edit", "-json")
	cmd.Dir = snapshot
	cmd.Env = append(os.Environ(), "GOWORK=off")
	raw, err := proc.Output(cmd)
	if err != nil {
		return false, fmt.Errorf("read main module replacement directives: %w", err)
	}
	var mod struct {
		Replace []struct {
			Old struct{ Path, Version string }
			New struct{ Path, Version string }
		}
	}
	if err := json.Unmarshal(raw, &mod); err != nil {
		return false, fmt.Errorf("decode main module replacements: %w", err)
	}
	hostReal, err := filepath.EvalSymlinks(hostRoot)
	if err != nil {
		return false, err
	}
	hostReal, err = filepath.Abs(hostReal)
	if err != nil {
		return false, err
	}
	destinationRoot := filepath.Join(snapshot, localReplacementsDir)
	externalCount := 0
	found := false
	for _, r := range mod.Replace {
		if r.New.Path == "" || r.New.Version != "" {
			continue
		}
		found = true
		source := r.New.Path
		if !filepath.IsAbs(source) {
			source = filepath.Join(hostRoot, source)
		}
		// Follow directory symlinks when determining whether the replacement
		// is actually inside the copied module, and reject non-directories.
		realSource, err := filepath.EvalSymlinks(source)
		if err != nil {
			return found, fmt.Errorf("resolve local replacement %q: %w", r.New.Path, err)
		}
		info, err := os.Stat(realSource)
		if err != nil {
			return found, err
		}
		if !info.IsDir() {
			return found, fmt.Errorf("local replacement %q is not a directory", source)
		}
		relative, err := filepath.Rel(hostReal, realSource)
		if err != nil {
			return found, fmt.Errorf("locate local replacement %q: %w", source, err)
		}
		var replacement string
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			// Even absolute in-module replacements must not point back into
			// the host. Keep the corresponding sandbox-relative path.
			if _, err := os.Stat(filepath.Join(snapshot, relative, "go.mod")); err != nil {
				return found, fmt.Errorf("local replacement %q is missing from sandbox snapshot (possibly excluded): %w", source, err)
			}
			replacement = "./" + filepath.ToSlash(relative)
			if relative == "." {
				replacement = "."
			}
		} else {
			// A relative --cache-dir inside the reserved snapshot subtree
			// would make CopyModule silently exclude replacement files when
			// cloning the snapshot for tests. Refuse this configuration.
			if !filepath.IsAbs(cacheDir) {
				relCache := filepath.Clean(cacheDir)
				if relCache == localReplacementsDir || strings.HasPrefix(relCache, localReplacementsDir+string(filepath.Separator)) {
					return found, fmt.Errorf("cache directory %q overlaps isolated local replacement snapshots", cacheDir)
				}
			}
			// An ancestor of the main module is not a safe replacement to
			// capture: copying it may recurse through arbitrary unrelated
			// projects or temporary sandbox data.
			if subpath, err := filepath.Rel(realSource, hostReal); err == nil && subpath != ".." && !strings.HasPrefix(subpath, ".."+string(filepath.Separator)) {
				return found, fmt.Errorf("local replacement %q contains the main module; cannot isolate ancestor directories safely", source)
			}
			if externalCount == 0 {
				if _, err := os.Lstat(destinationRoot); err == nil {
					return found, fmt.Errorf("reserved directory %q already exists in input module", localReplacementsDir)
				} else if !os.IsNotExist(err) {
					return found, err
				}
			}
			if err := os.MkdirAll(destinationRoot, 0o755); err != nil {
				return found, err
			}
			name := fmt.Sprintf("dep-%04d", externalCount)
			externalCount++
			// CopyModule rejects outward symlinks and copies independent
			// bytes. No test ever runs from or writes into the original.
			copied, cleanup, err := CopyModule(realSource, cacheDir)
			if err != nil {
				return found, fmt.Errorf("snapshot local replacement %q: %w", source, err)
			}
			// Reject input edits that overlap the external dependency copy.
			// Otherwise an arbitrary mixture of dependency versions could
			// be reused for every mutant as though it were one snapshot.
			if err := VerifyModuleCopy(realSource, copied, cacheDir); err != nil {
				cleanup()
				return found, fmt.Errorf("local replacement %q changed during acquisition: %w", source, err)
			}
			dest := filepath.Join(destinationRoot, name)
			err = os.Rename(copied, dest)
			cleanup()
			if err != nil {
				return found, fmt.Errorf("materialize local replacement %q: %w", source, err)
			}
			replacement = "./" + localReplacementsDir + "/" + name
		}
		old := r.Old.Path
		if r.Old.Version != "" {
			old += "@" + r.Old.Version
		}
		if old == "" {
			return found, fmt.Errorf("local replacement has no original module path")
		}
		edit := proc.Command(ctx, "go", "mod", "edit", "-replace="+old+"="+replacement)
		edit.Dir = snapshot
		edit.Env = append(os.Environ(), "GOWORK=off")
		if out, err := proc.CombinedOutput(edit); err != nil {
			return found, fmt.Errorf("rewrite sandbox replacement %q: %w: %s", old, err, out)
		}
	}
	return found, nil
}
