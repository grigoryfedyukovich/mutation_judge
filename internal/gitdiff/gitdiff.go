package gitdiff

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/example/mutation-judge/internal/proc"
)

// The diff is requested with --unified=0. Keep both sides of the hunk so
// source lines that resemble diff headers cannot be mistaken for metadata.
var hunkRE = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?:.*)$`)

func ChangedLines(root, base string) (map[string]map[int]bool, error) {
	return ChangedLinesContext(context.Background(), root, base)
}

// ChangedLinesContext allows interruption during incremental preparation.
// Non-ignored untracked Go files use line 0 as a whole-file change marker.
// Explicit diff options override user/repository settings that would alter
// the format of machine-consumed headers. Git still C-quotes some pathnames
// even with core.quotepath=false (quotes, backslashes and control bytes).
func ChangedLinesContext(ctx context.Context, root, base string) (map[string]map[int]bool, error) {
	cmd := proc.Command(ctx, "git", "-c", "core.quotepath=false", "diff",
		"--no-color", "--src-prefix=a/", "--dst-prefix=b/",
		"--unified=0", "--no-ext-diff", "--no-textconv",
		base, "--", ":(glob)**/*.go")
	cmd.Dir = root
	out, err := proc.CombinedOutput(cmd)
	if err != nil {
		return nil, fmt.Errorf("git diff %s failed: %w\n%s", base, err, strings.TrimSpace(string(out)))
	}
	lines, err := Parse(out)
	if err != nil {
		return nil, fmt.Errorf("parse git diff %s: %w", base, err)
	}
	// Untracked working-tree sources are invisible to git diff, but are
	// compiled by Go and must be selected in incremental analysis. -z
	// avoids ambiguity for quoted, backslash, and newline-containing paths.
	untracked := proc.Command(ctx, "git", "ls-files", "--others", "--exclude-standard", "-z",
		"--", ":(glob)**/*.go")
	untracked.Dir = root
	paths, err := proc.CombinedOutput(untracked)
	if err != nil {
		return nil, fmt.Errorf("git ls-files (untracked Go files) failed: %w\n%s", err, strings.TrimSpace(string(paths)))
	}
	for _, item := range bytes.Split(paths, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		path := string(item)
		if !strings.HasSuffix(path, ".go") || strings.HasPrefix(path, "/") ||
			path == "." || path == ".." || strings.HasPrefix(path, "../") ||
			strings.Contains(path, "/../") || strings.HasPrefix(path, "./") {
			return nil, fmt.Errorf("invalid untracked Go path %q", path)
		}
		// Physical source lines start at 1. Line 0 marks the entire
		// file changed, without allocating one entry per source line or
		// rereading live files outside the immutable input snapshot.
		lines[path] = map[int]bool{0: true}
	}
	return lines, nil
}

// decodePath decodes the C-quoted paths that Git uses in --- and +++ lines.
// strconv.Unquote supports Git's three-digit octal escapes, including bytes
// of multibyte UTF-8 paths. An unquoted name is used verbatim.
func decodePath(header, prefix string) (string, error) {
	path := header
	if strings.HasPrefix(path, `"`) {
		var err error
		path, err = strconv.Unquote(path)
		if err != nil {
			return "", fmt.Errorf("malformed quoted Git path %q: %w", header, err)
		}
	}
	if path == "/dev/null" {
		return "", nil
	}
	if !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("unexpected Git path %q (expected %q prefix)", path, prefix)
	}
	name := strings.TrimPrefix(path, prefix)
	if name == "" || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid Git path %q", path)
	}
	return name, nil
}

func positiveCount(value string) (int, error) {
	if value == "" {
		return 1, nil
	}
	return strconv.Atoi(value)
}

// Parse extracts changed new-side line numbers from a unified zero-context
// Git diff. It rejects malformed or unrecognized text rather than silently
// returning an empty changed set (which would hide real mutations).
func Parse(data []byte) (map[string]map[int]bool, error) {
	changed := make(map[string]map[int]bool)
	if len(bytes.TrimSpace(data)) == 0 {
		return changed, nil // An empty diff is legitimate.
	}

	var file string
	var inDiff, oldHeader, newHeader, binaryPatch, hunkSeen, metadataOnlyChange bool
	var oldRemaining, newRemaining int
	var lineNum int
	checkFile := func() error {
		if oldHeader != newHeader {
			return fmt.Errorf("incomplete file headers (--- and +++ must occur together)")
		}
		if !oldHeader && !metadataOnlyChange {
			return fmt.Errorf("file has no text headers or recognized metadata-only change")
		}
		if oldRemaining != 0 || newRemaining != 0 {
			return fmt.Errorf("truncated hunk (remaining old=%d new=%d)", oldRemaining, newRemaining)
		}
		return nil
	}

	s := bufio.NewScanner(bytes.NewReader(data))
	// Avoid Scan's default 64 KiB line limit, especially for diff payload
	// lines. Oversized output is an explicit error, not an empty changed set.
	s.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for s.Scan() {
		lineNum++
		line := s.Text()
		fail := func(format string, args ...any) error {
			return fmt.Errorf("diff line %d: %s", lineNum, fmt.Sprintf(format, args...))
		}

		// Consume hunk payload before looking for headers: e.g. a source
		// line beginning "++ " is emitted as "+++ ".
		if oldRemaining > 0 || newRemaining > 0 {
			if line == `\ No newline at end of file` {
				continue
			}
			if len(line) == 0 {
				return nil, fail("empty line inside hunk")
			}
			switch line[0] {
			case '+':
				newRemaining--
			case '-':
				oldRemaining--
			case ' ':
				oldRemaining--
				newRemaining--
			default:
				return nil, fail("unexpected hunk line %q", line)
			}
			if oldRemaining < 0 || newRemaining < 0 {
				return nil, fail("hunk line count exceeded")
			}
			continue
		}

		if strings.HasPrefix(line, "diff --git ") {
			if inDiff {
				if err := checkFile(); err != nil {
					return nil, fail("previous file: %v", err)
				}
			}
			inDiff, oldHeader, newHeader, binaryPatch, hunkSeen, metadataOnlyChange = true, false, false, false, false, false
			file = ""
			continue
		}
		if !inDiff {
			return nil, fail("expected diff --git header, got %q", line)
		}
		if binaryPatch {
			// Binary patches have no meaningful Go source line numbers.
			// They may contain arbitrary base85-encoded payload lines.
			continue
		}
		if line == `\ No newline at end of file` && hunkSeen {
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			if oldHeader || newHeader || hunkSeen {
				return nil, fail("unexpected repeated --- file header")
			}
			if _, err := decodePath(strings.TrimPrefix(line, "--- "), "a/"); err != nil {
				return nil, fail("old file header: %v", err)
			}
			oldHeader = true
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			if !oldHeader || newHeader {
				return nil, fail("+++ file header without matching ---")
			}
			var err error
			file, err = decodePath(strings.TrimPrefix(line, "+++ "), "b/")
			if err != nil {
				return nil, fail("new file header: %v", err)
			}
			newHeader = true
			continue
		}
		if strings.HasPrefix(line, "@@") {
			if !newHeader {
				return nil, fail("hunk without a valid +++ file header")
			}
			m := hunkRE.FindStringSubmatch(line)
			if m == nil {
				return nil, fail("invalid hunk header %q", line)
			}
			start, err := strconv.Atoi(m[3])
			if err != nil {
				return nil, fail("invalid new-side start: %v", err)
			}
			oldRemaining, err = positiveCount(m[2])
			if err != nil {
				return nil, fail("invalid old-side count: %v", err)
			}
			newRemaining, err = positiveCount(m[4])
			if err != nil {
				return nil, fail("invalid new-side count: %v", err)
			}
			hunkSeen = true
			if file != "" && newRemaining > 0 {
				if changed[file] == nil {
					changed[file] = make(map[int]bool)
				}
				for i := 0; i < newRemaining; i++ {
					changed[file][start+i] = true
				}
			}
			continue
		}
		// Ordinary metadata and binary/no-content changes are valid.
		if line == "GIT binary patch" {
			binaryPatch = true
			metadataOnlyChange = true
			continue
		}
		if strings.HasPrefix(line, "Binary files ") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "old mode ") ||
			strings.HasPrefix(line, "new mode ") ||
			strings.HasPrefix(line, "new file mode ") ||
			strings.HasPrefix(line, "deleted file mode ") ||
			strings.HasPrefix(line, "similarity index ") ||
			strings.HasPrefix(line, "dissimilarity index ") ||
			strings.HasPrefix(line, "rename from ") ||
			strings.HasPrefix(line, "rename to ") ||
			strings.HasPrefix(line, "copy from ") ||
			strings.HasPrefix(line, "copy to ") {
			if !strings.HasPrefix(line, "index ") {
				metadataOnlyChange = true
			}
			continue
		}
		return nil, fail("unrecognized diff line %q", line)
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("read Git diff: %w", err)
	}
	if err := checkFile(); err != nil {
		return nil, fmt.Errorf("last file: %w", err)
	}
	return changed, nil
}
