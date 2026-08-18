package gitdiff

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var hunkRE = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,([0-9]+))? @@`)

func ChangedLines(root, base string) (map[string]map[int]bool, error) {
	// Git pathspecs are recursive, but use explicit glob magic so this cannot be
	// mistaken for a shell glob by future maintainers.
	cmd := exec.Command("git", "diff", "--unified=0", "--no-ext-diff", base, "--", ":(glob)**/*.go")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff %s failed: %w\n%s", base, err, strings.TrimSpace(string(out)))
	}
	return Parse(out)
}

func Parse(data []byte) (map[string]map[int]bool, error) {
	result := map[string]map[int]bool{}
	var file string
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "+++ ") {
			name := strings.TrimPrefix(line, "+++ ")
			if name == "/dev/null" {
				file = ""
			} else if strings.HasPrefix(name, "b/") {
				file = filepath.ToSlash(strings.TrimPrefix(name, "b/"))
			} else {
				file = ""
			}
			continue
		}
		m := hunkRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if file == "" || file == "/dev/null" {
			continue
		}
		start, _ := strconv.Atoi(m[1])
		count := 1
		if m[2] != "" {
			count, _ = strconv.Atoi(m[2])
		}
		if count == 0 {
			continue
		}
		if result[file] == nil {
			result[file] = map[int]bool{}
		}
		for i := 0; i < count; i++ {
			result[file][start+i] = true
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
