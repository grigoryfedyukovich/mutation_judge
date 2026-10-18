package coverage

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Map struct {
	Lines       map[string]map[int]bool
	BySuffix    map[string]map[int]bool
	ambiguous   map[string]bool
	suffixOwner map[string]string
}

// Parse loads a Go coverage profile. Every record must be valid: silently
// skipping malformed rows could make an observing test appear uncovered and
// make --coverage-test-selection omit it. Zero-count blocks still register
// their file as instrumented, distinguishing known-uncovered from unknown.
func Parse(path, moduleRoot string) (Map, error) {
	f, err := os.Open(path)
	if err != nil {
		return Map{}, err
	}
	defer f.Close()
	m := Map{
		Lines: map[string]map[int]bool{}, BySuffix: map[string]map[int]bool{},
		ambiguous: map[string]bool{}, suffixOwner: map[string]string{},
	}
	s := bufio.NewScanner(f)
	// The default Scanner limit is too small for generated paths in some
	// workspaces; reject genuinely oversized lines as an explicit error.
	s.Buffer(make([]byte, 64*1024), 2*1024*1024)
	lineNumber := 0
	mode := ""
	for s.Scan() {
		lineNumber++
		line := strings.TrimSpace(s.Text())
		if lineNumber == 1 {
			switch line {
			case "mode: set", "mode: count", "mode: atomic":
				mode = strings.TrimPrefix(line, "mode: ")
			default:
				return Map{}, fmt.Errorf("coverage profile %s:%d: invalid mode header %q", path, lineNumber, line)
			}
			continue
		}
		file, start, end, count, err := parseBlock(line, mode)
		if err != nil {
			return Map{}, fmt.Errorf("coverage profile %s:%d: %w", path, lineNumber, err)
		}
		rel := normalize(file, moduleRoot)
		if m.Lines[rel] == nil {
			m.Lines[rel] = map[int]bool{}
		}
		if count > 0 {
			for l := start; l <= end; l++ {
				m.Lines[rel][l] = true
			}
		}
	}
	if err := s.Err(); err != nil {
		return Map{}, fmt.Errorf("read coverage profile %s: %w", path, err)
	}
	if lineNumber == 0 {
		return Map{}, fmt.Errorf("coverage profile %s: missing mode header", path)
	}
	m.buildSuffixIndex()
	return m, nil
}

// parseBlock validates all four fields of Go's coverprofile block format:
// file:start.line,end.line statements count. The maximum block span is a
// defensive guard against corrupted input requiring enormous allocations.
// A legitimate Go basic block should never span a million source lines.
func parseBlock(line, mode string) (file string, startLine, endLine, count int, err error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return "", 0, 0, 0, fmt.Errorf("expected coverage span, statement count, and execution count, got %q", line)
	}
	colon := strings.LastIndexByte(fields[0], ':')
	if colon <= 0 || colon == len(fields[0])-1 {
		return "", 0, 0, 0, fmt.Errorf("invalid coverage file/span %q", fields[0])
	}
	file = fields[0][:colon]
	rangePart := fields[0][colon+1:]
	start, end, ok := strings.Cut(rangePart, ",")
	if !ok || strings.Contains(end, ",") {
		return "", 0, 0, 0, fmt.Errorf("invalid coverage range %q", rangePart)
	}
	startLine, startCol, err := parsePosition(start)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("invalid start position: %w", err)
	}
	endLine, endCol, err := parsePosition(end)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("invalid end position: %w", err)
	}
	if endLine < startLine || (endLine == startLine && endCol <= startCol) {
		return "", 0, 0, 0, fmt.Errorf("inverted or empty coverage range %q", rangePart)
	}
	if endLine-startLine > 1_000_000 {
		return "", 0, 0, 0, fmt.Errorf("coverage range %q exceeds supported block size", rangePart)
	}
	statements, err := strconv.Atoi(fields[1])
	if err != nil || statements < 0 {
		return "", 0, 0, 0, fmt.Errorf("invalid statement count %q", fields[1])
	}
	count, err = strconv.Atoi(fields[2])
	if err != nil || count < 0 || (mode == "set" && count > 1) {
		return "", 0, 0, 0, fmt.Errorf("invalid execution count %q for mode %s", fields[2], mode)
	}
	return file, startLine, endLine, count, nil
}

func parsePosition(position string) (line, column int, err error) {
	lineText, columnText, ok := strings.Cut(position, ".")
	if !ok || strings.Contains(columnText, ".") {
		return 0, 0, fmt.Errorf("expected line.column, got %q", position)
	}
	line, err = strconv.Atoi(lineText)
	if err != nil || line <= 0 {
		return 0, 0, fmt.Errorf("invalid line %q", lineText)
	}
	column, err = strconv.Atoi(columnText)
	if err != nil || column <= 0 {
		return 0, 0, fmt.Errorf("invalid column %q", columnText)
	}
	return line, column, nil
}

// buildSuffixIndex indexes every path-suffix of each coverage file key so
// that a shorter caller-supplied relative path (e.g. "pkg/file.go") can
// resolve against a longer module-path-prefixed coverage key (e.g.
// "example.com/mod/pkg/file.go"). A suffix is ambiguous, and therefore
// excluded from BySuffix, as soon as it is claimed by more than one
// distinct full file key — regardless of whether those files' covered
// line sets happen to coincide. Line-set content is not a proxy for file
// identity: two unrelated files can easily cover the same line numbers
// (e.g. two small functions with the same statement/block shape), and
// treating that coincidence as "unambiguous" would let the index silently
// resolve a query to the wrong file's coverage data.
func (m *Map) buildSuffixIndex() {
	if m.suffixOwner == nil {
		m.suffixOwner = map[string]string{}
	}
	for file, lines := range m.Lines {
		parts := strings.Split(filepath.ToSlash(file), "/")
		for i := range parts {
			suffix := strings.Join(parts[i:], "/")
			if m.ambiguous[suffix] {
				continue
			}
			if owner, ok := m.suffixOwner[suffix]; ok && owner != file {
				delete(m.BySuffix, suffix)
				delete(m.suffixOwner, suffix)
				m.ambiguous[suffix] = true
				continue
			}
			m.suffixOwner[suffix] = file
			m.BySuffix[suffix] = lines
		}
	}
}

func normalize(file, root string) string {
	file = filepath.Clean(file)
	if filepath.IsAbs(file) {
		if rel, err := filepath.Rel(root, file); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	// Go coverage often uses module/import paths. Keep progressively useful suffixes.
	return filepath.ToSlash(file)
}

func (m Map) Covered(file string, start, end int) (bool, bool) {
	file = filepath.ToSlash(file)
	lines, ok := m.Lines[file]
	if !ok {
		lines, ok = m.BySuffix[file]
	}
	if !ok {
		return false, false
	}
	for l := start; l <= end; l++ {
		if lines[l] {
			return true, true
		}
	}
	return false, true
}
