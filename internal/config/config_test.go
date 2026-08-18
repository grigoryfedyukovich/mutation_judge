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
		{"yaml-block-list", "operators:\n  - boundary\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := ".toml"
			if tc.name == "yaml-block-list" {
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
