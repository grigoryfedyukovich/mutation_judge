package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestR04TimeoutConfig(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "mutation-judge.toml")
	data := "preparation_timeout = \"45s\"\ntotal_timeout = \"4m\"\n"
	if err := os.WriteFile(filename, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(filename, Default())
	if err != nil {
		t.Fatal(err)
	}
	if got.PreparationTimeout != 45*time.Second || got.TotalTimeout != 4*time.Minute {
		t.Fatalf("bad timeouts: %+v", got)
	}
	if got.AsMap()["total_timeout"] != "4m0s" {
		t.Fatalf("missing total timeout in effective config: %v", got.AsMap())
	}
	got.TotalTimeout = -time.Second
	if Validate(got) == nil {
		t.Fatal("negative total timeout should fail")
	}
	got.TotalTimeout = 0
	got.PreparationTimeout = -time.Second
	if Validate(got) == nil {
		t.Fatal("negative prep timeout should fail")
	}
}
