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
}

func Default() Config {
	return Config{
		Operators:  []string{"boundary", "boolean"},
		Timeout:    20 * time.Second,
		Format:     "text",
		CacheDir:   ".mutation-judge/cache",
		Cache:      true,
		CIExitCode: 10,
		Progress:   true,
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

// Load accepts a deliberately strict flat subset of TOML or YAML. Nested
// tables/maps, block lists, anchors, and multiline strings are rejected rather
// than guessed. This keeps the dependency-free MVP deterministic while making
// unsupported syntax fail visibly.
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

	cfg := base
	s := bufio.NewScanner(f)
	lineNo := 0
	for s.Scan() {
		lineNo++
		line, err := stripComment(s.Text())
		if err != nil {
			return cfg, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		line = strings.TrimSpace(line)
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
		if key == "" || value == "" {
			return cfg, fmt.Errorf("%s:%d: key and value must be non-empty", path, lineNo)
		}
		if err := set(&cfg, key, value); err != nil {
			return cfg, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
	}
	if err := s.Err(); err != nil {
		return cfg, err
	}
	return cfg, Validate(cfg)
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
			return strings.TrimSpace(s[:i]), nil
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
		c.Operators = v
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
	default:
		return fmt.Errorf("unknown configuration key %q", key)
	}
	return nil
}

func Validate(c Config) error {
	known := map[string]bool{"boundary": true, "boolean": true, "arithmetic": true}
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
	if c.Format != "text" && c.Format != "json" && c.Format != "html" {
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
	return nil
}
