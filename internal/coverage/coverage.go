package coverage

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Map struct {
	Lines     map[string]map[int]bool
	BySuffix  map[string]map[int]bool
	ambiguous map[string]bool
}

func Parse(path, moduleRoot string) (Map, error) {
	f, err := os.Open(path)
	if err != nil {
		return Map{}, err
	}
	defer f.Close()
	m := Map{Lines: map[string]map[int]bool{}, BySuffix: map[string]map[int]bool{}, ambiguous: map[string]bool{}}
	s := bufio.NewScanner(f)
	first := true
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if first {
			first = false
			if strings.HasPrefix(line, "mode:") {
				continue
			}
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil || count <= 0 {
			continue
		}
		colon := strings.LastIndex(fields[0], ":")
		if colon < 0 {
			continue
		}
		file := fields[0][:colon]
		rangePart := fields[0][colon+1:]
		parts := strings.Split(rangePart, ",")
		if len(parts) != 2 {
			continue
		}
		start := strings.Split(parts[0], ".")
		end := strings.Split(parts[1], ".")
		if len(start) != 2 || len(end) != 2 {
			continue
		}
		a, _ := strconv.Atoi(start[0])
		b, _ := strconv.Atoi(end[0])
		rel := normalize(file, moduleRoot)
		if m.Lines[rel] == nil {
			m.Lines[rel] = map[int]bool{}
		}
		for l := a; l <= b; l++ {
			m.Lines[rel][l] = true
		}
	}
	if err := s.Err(); err != nil {
		return Map{}, err
	}
	m.buildSuffixIndex()
	return m, nil
}

func (m *Map) buildSuffixIndex() {
	for file, lines := range m.Lines {
		parts := strings.Split(filepath.ToSlash(file), "/")
		for i := range parts {
			suffix := strings.Join(parts[i:], "/")
			if m.ambiguous[suffix] {
				continue
			}
			if existing, ok := m.BySuffix[suffix]; ok && !sameLines(existing, lines) {
				delete(m.BySuffix, suffix)
				m.ambiguous[suffix] = true
				continue
			}
			m.BySuffix[suffix] = lines
		}
	}
}

func sameLines(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for line := range a {
		if !b[line] {
			return false
		}
	}
	return true
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
