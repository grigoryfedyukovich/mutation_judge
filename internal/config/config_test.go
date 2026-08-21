package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStrictTOML(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "mutation-judge.toml")
	if err := os.WriteFile(p, []byte("operators = [\"boundary\", \"arithmetic\"]\ntimeout = \"3s\"\ncache = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, Default())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout.String() != "3s" || cfg.Cache || len(cfg.Operators) != 2 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestUnknownKeyIsError(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "mutation-judge.yaml")
	_ = os.WriteFile(p, []byte("mystery: true\n"), 0o644)
	if _, err := Load(p, Default()); err == nil {
		t.Fatal("expected error")
	}
}

func TestFindAndLoadSearchesToModuleRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mutation-judge.toml"), []byte("timeout = \"7s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, path, err := FindAndLoad(sub)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout.String() != "7s" || path == "" {
		t.Fatalf("did not load project config: %#v %q", cfg, path)
	}
}

func TestCommentParsingKeepsApostropheAndHashInsideDoubleQuotes(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "mutation-judge.toml")
	data := "test_run = \"TestIt's # Fine\" # actual comment\n"
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, Default())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TestRun != "TestIt's # Fine" {
		t.Fatalf("unexpected test_run: %q", cfg.TestRun)
	}
}

func TestUnsupportedNestedSyntaxIsRejected(t *testing.T) {
	d := t.TempDir()
	for _, tc := range []struct {
		name, data string
	}{
		{"toml-table", "[report]\nformat = \"json\"\n"},
		{"yaml-nested-map", "report:\n  format: json\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := ".toml"
			if tc.name == "yaml-nested-map" {
				ext = ".yaml"
			}
			p := filepath.Join(d, tc.name+ext)
			if err := os.WriteFile(p, []byte(tc.data), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p, Default()); err == nil {
				t.Fatal("expected strict subset error")
			}
		})
	}
}

// See docs/decisions/0001-config-parser-scope.md: YAML's native
// block-list style is accepted for list-valued keys specifically,
// because it is unambiguous to parse for a single flat list without
// adopting general nested-YAML parsing. Genuine nested maps/tables
// (above) remain rejected.
func TestYAMLBlockListIsAcceptedForListKeys(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "mutation-judge.yaml")
	data := "operators:\n  - boundary\n  - arithmetic # trailing comment\n  - \"boolean\"\ntimeout: \"9s\"\n"
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, Default())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"boundary", "arithmetic", "boolean"}
	if len(cfg.Operators) != len(want) {
		t.Fatalf("operators = %v, want %v", cfg.Operators, want)
	}
	for i := range want {
		if cfg.Operators[i] != want[i] {
			t.Fatalf("operators = %v, want %v", cfg.Operators, want)
		}
	}
	// The key after the block list must still parse normally: proves the
	// lookahead correctly resumes the main loop past the consumed lines.
	if cfg.Timeout.String() != "9s" {
		t.Fatalf("timeout = %v, want 9s", cfg.Timeout)
	}
}

func TestYAMLBlockListRejectsNestedItems(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "mutation-judge.yaml")
	data := "operators:\n  - [boundary]\n"
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, Default()); err == nil {
		t.Fatal("expected an error for a nested list item")
	}
}

// An empty YAML value with nothing following it (not even an
// unindented next line) is still an ordinary empty-value error, not
// silently treated as an empty list.
func TestYAMLEmptyValueWithNoBlockListIsStillAnError(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "mutation-judge.yaml")
	if err := os.WriteFile(p, []byte("test_run:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, Default()); err == nil {
		t.Fatal("expected an error for an empty scalar value")
	}
}
