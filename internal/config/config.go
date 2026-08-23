package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Operators        []string
	Timeout          time.Duration
	TestRun          string
	Format           string
	Output           string
	CacheDir         string
	Cache            bool
	MaxMutants       int
	CIMinScore       float64
	CIExitCode       int
	IncludeGenerated bool
	ChangedBase      string
	Progress         bool
	NarrowTestScope  bool
	Workers          int
}

func Default() Config {
	return Config{
		Operators:  []string{"boundary", "boolean"},
		Timeout:    20 * time.Second,
		CacheDir:   ".mutation-judge/cache",
		Cache:      true,
		CIExitCode: 10,
		Format:     "text",
		Progress:   true,
		Workers:    1,
	}
}

func (c Config) AsMap() map[string]any {
	return map[string]any{
		"operators":         c.Operators,
		"timeout":           c.Timeout.String(),
		"test_run":          c.TestRun,
		"format":            c.Format,
		"output":            c.Output,
		"cache_dir":         c.CacheDir,
		"cache":             c.Cache,
		"max_mutants":       c.MaxMutants,
		"ci_min_score":      c.CIMinScore,
		"ci_exit_code":      c.CIExitCode,
		"include_generated": c.IncludeGenerated,
		"changed":           c.ChangedBase,
		"progress":          c.Progress,
		"narrow_test_scope": c.NarrowTestScope,
		"workers":           c.Workers,
	}
}

func FindAndLoad(start string) (Config, string, error) {
	cfg := Default()
	dir, err := filepath.Abs(start)
	if err != nil {
		return cfg, "", err
	}
	names := []string{"mutation-judge.toml", ".mutation-judge.toml", "mutation-judge.yaml", ".mutation-judge.yaml", "mutation-judge.yml", ".mutation-judge.yml"}
	for {
		for _, name := range names {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				loaded, err := Load(path, cfg)
				return loaded, path, err
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return cfg, "", nil
}

// Load accepts a deliberately strict flat subset of TOML or YAML: one key
// value pair per line, scalars and single-level [a, b, c] lists, quoted
// strings, and (for YAML only) a list-valued key followed by its items in
// YAML's native block-list style. Nested tables/maps, YAML anchors, and
// multiline strings are rejected rather than guessed. See
// docs/decisions/0001-config-parser-scope.md for why this is the
// permanent design rather than an MVP placeholder for a full TOML/YAML
// library.
func Load(path string, base Config) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return base, err
	}
	defer f.Close()

	isYAML := strings.HasSuffix(strings.ToLower(path), ".yaml") || strings.HasSuffix(strings.ToLower(path), ".yml")
	separator := "="
	if isYAML {
		separator = ":"
	}

	var rawLines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		rawLines = append(rawLines, s.Text())
	}
	if err := s.Err(); err != nil {
		return base, err
	}

	cfg := base
	for i := 0; i < len(rawLines); i++ {
		lineNo := i + 1
		raw, err := stripComment(rawLines[i])
		if err != nil {
			return cfg, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "{") {
			return cfg, fmt.Errorf("%s:%d: nested or block configuration syntax is unsupported; use one flat key %s value per line", path, lineNo, separator)
		}
		parts := splitOutsideQuotes(line, separator)
		if len(parts) != 2 {
			return cfg, fmt.Errorf("%s:%d: expected exactly one key %s value separator", path, lineNo, separator)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return cfg, fmt.Errorf("%s:%d: key and value must be non-empty", path, lineNo)
		}
		if value == "" && isYAML {
			// A YAML key with nothing after the colon is only accepted
			// when followed by its items in native block-list style
			// (each subsequent line indented deeper than this key and
			// starting with "- "); anything else -- a nested map, or
			// simply nothing -- is still rejected below.
			items, consumed, ok, err := readYAMLBlockList(rawLines, i+1, indent)
			if err != nil {
				return cfg, fmt.Errorf("%s:%d: %w", path, i+2, err)
			}
			if ok {
				if err := setList(&cfg, key, items); err != nil {
					return cfg, fmt.Errorf("%s:%d: %w", path, lineNo, err)
				}
				i += consumed
				continue
			}
		}
		if value == "" {
			return cfg, fmt.Errorf("%s:%d: key and value must be non-empty", path, lineNo)
		}
		if err := set(&cfg, key, value); err != nil {
			return cfg, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
	}
	return cfg, Validate(cfg)
}

// readYAMLBlockList looks for a run of "- item" lines, each indented
// deeper than parentIndent, starting at rawLines[start]. It returns the
// unquoted item values, how many lines were consumed, and whether a
// block list was found at all (ok is false, with no error, if the very
// next line doesn't start a block list -- that's not this function's
// problem to report, the caller decides what an empty value with no
// following list means).
func readYAMLBlockList(rawLines []string, start, parentIndent int) (items []string, consumed int, ok bool, err error) {
	i := start
	for i < len(rawLines) {
		raw, cerr := stripComment(rawLines[i])
		if cerr != nil {
			return nil, 0, false, cerr
		}
		if strings.TrimSpace(raw) == "" {
			break
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		trimmed := strings.TrimSpace(raw)
		if indent <= parentIndent || !strings.HasPrefix(trimmed, "-") {
			break
		}
		item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		if item == "" || strings.ContainsAny(item, "[]{}") {
			return nil, 0, false, errors.New("nested list/map values are unsupported")
		}
		q, uerr := unquote(item)
		if uerr != nil {
			return nil, 0, false, uerr
		}
		if q == "" {
			return nil, 0, false, errors.New("list entries must be non-empty")
		}
		items = append(items, q)
		i++
	}
	if len(items) == 0 {
		return nil, 0, false, nil
	}
	return items, i - start, true, nil
}

func stripComment(s string) (string, error) {
	var quote rune
	escaped := false
	for i, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '"', '\'':
			quote = r
		case '#':
			// Trailing whitespace only: leading whitespace is kept so
			// callers can still measure indentation (needed for YAML
			// block-list detection), matching what strings.TrimSpace(s)
			// would have discarded from both ends before this change.
			return strings.TrimRight(s[:i], " \t"), nil
		}
	}
	if quote != 0 {
		return "", errors.New("unterminated quoted value")
	}
	return s, nil
}

func splitOutsideQuotes(s, separator string) []string {
	var quote rune
	escaped := false
	for i, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			continue
		}
		if strings.HasPrefix(s[i:], separator) {
			return []string{s[:i], s[i+len(separator):]}
		}
	}
	return []string{s}
}

func unquote(v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) < 2 {
		return v, nil
	}
	if v[0] == '"' {
		if v[len(v)-1] != '"' {
			return "", errors.New("unterminated double-quoted string")
		}
		q, err := strconv.Unquote(v)
		if err != nil {
			return "", fmt.Errorf("invalid quoted string: %w", err)
		}
		return q, nil
	}
	if v[0] == '\'' {
		if v[len(v)-1] != '\'' {
			return "", errors.New("unterminated single-quoted string")
		}
		return v[1 : len(v)-1], nil
	}
	return v, nil
}

func parseList(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, errors.New("list value must use [item, item] syntax")
	}
	v = strings.TrimSpace(v[1 : len(v)-1])
	if v == "" {
		return nil, nil
	}
	parts, err := splitList(v)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		q, err := unquote(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		if q == "" {
			return nil, errors.New("list entries must be non-empty")
		}
		out = append(out, q)
	}
	return out, nil
}

func splitList(v string) ([]string, error) {
	var parts []string
	start := 0
	var quote rune
	escaped := false
	for i, r := range v {
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '"', '\'':
			quote = r
		case ',':
			parts = append(parts, v[start:i])
			start = i + 1
		case '[', ']', '{', '}':
			return nil, errors.New("nested list/map values are unsupported")
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quoted list entry")
	}
	parts = append(parts, v[start:])
	return parts, nil
}

func set(c *Config, key, value string) error {
	parseString := func() (string, error) { return unquote(value) }
	parseBool := func() (bool, error) {
		v, err := parseString()
		if err != nil {
			return false, err
		}
		return strconv.ParseBool(v)
	}
	parseInt := func() (int, error) {
		v, err := parseString()
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(v)
	}
	parseFloat := func() (float64, error) {
		v, err := parseString()
		if err != nil {
			return 0, err
		}
		return strconv.ParseFloat(v, 64)
	}

	switch key {
	case "operators":
		v, err := parseList(value)
		if err != nil {
			return err
		}
		return setList(c, key, v)
	case "timeout":
		v, err := parseString()
		if err != nil {
			return err
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid timeout: %w", err)
		}
		c.Timeout = d
	case "test_run":
		v, err := parseString()
		if err != nil {
			return err
		}
		c.TestRun = v
	case "format":
		v, err := parseString()
		if err != nil {
			return err
		}
		c.Format = v
	case "output":
		v, err := parseString()
		if err != nil {
			return err
		}
		c.Output = v
	case "cache_dir":
		v, err := parseString()
		if err != nil {
			return err
		}
		c.CacheDir = v
	case "cache":
		v, err := parseBool()
		if err != nil {
			return err
		}
		c.Cache = v
	case "max_mutants":
		v, err := parseInt()
		if err != nil {
			return err
		}
		c.MaxMutants = v
	case "ci_min_score":
		v, err := parseFloat()
		if err != nil {
			return err
		}
		c.CIMinScore = v
	case "ci_exit_code":
		v, err := parseInt()
		if err != nil {
			return err
		}
		c.CIExitCode = v
	case "include_generated":
		v, err := parseBool()
		if err != nil {
			return err
		}
		c.IncludeGenerated = v
	case "changed":
		v, err := parseString()
		if err != nil {
			return err
		}
		c.ChangedBase = v
	case "progress":
		v, err := parseBool()
		if err != nil {
			return err
		}
		c.Progress = v
	case "narrow_test_scope":
		v, err := parseBool()
		if err != nil {
			return err
		}
		c.NarrowTestScope = v
	case "workers":
		v, err := parseInt()
		if err != nil {
			return err
		}
		c.Workers = v
	default:
		return fmt.Errorf("unknown configuration key %q", key)
	}
	return nil
}

// setList assigns an already-split list of items to a list-typed
// configuration key, used both by set (for the inline `[a, b, c]` form)
// and by Load's YAML block-list handling. Adding a new list-typed key in
// the future means adding one case here.
func setList(c *Config, key string, items []string) error {
	switch key {
	case "operators":
		c.Operators = items
		return nil
	default:
		return fmt.Errorf("%q does not accept a list value", key)
	}
}

func Validate(c Config) error {
	known := map[string]bool{"boundary": true, "boolean": true, "arithmetic": true, "errorreturn": true, "switch": true, "loop": true, "channel": true}
	if len(c.Operators) == 0 {
		return errors.New("at least one mutation operator is required")
	}
	for _, op := range c.Operators {
		if !known[op] {
			return fmt.Errorf("unknown operator %q", op)
		}
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	switch c.Format {
	case "text", "json", "html", "sarif", "github":
	default:
		return fmt.Errorf("unsupported format %q", c.Format)
	}
	if c.MaxMutants < 0 {
		return errors.New("max_mutants cannot be negative")
	}
	if c.CIMinScore < 0 || c.CIMinScore > 100 {
		return errors.New("ci_min_score must be between 0 and 100")
	}
	if c.CIExitCode <= 0 || c.CIExitCode > 125 {
		return errors.New("ci_exit_code must be between 1 and 125")
	}
	if c.Workers < 0 {
		return errors.New("workers cannot be negative")
	}
	return nil
}
