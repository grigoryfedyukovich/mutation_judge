package cache

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
)

func TestStoreRoundTripAndSchemaRejection(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: dir, Enabled: true}
	key := Key("abc")
	want := runner.Result{Verdict: model.VerdictKilled, Tests: []string{"TestA"}, ExitCode: 1}
	if err := s.Put(key, want); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(key)
	if !ok || got.Verdict != want.Verdict || len(got.Tests) != 1 || got.Tests[0] != "TestA" {
		t.Fatalf("unexpected cache result: %#v, hit=%v", got, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, Key("bad")+".json"), []byte(`{"schema":"old","result":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(Key("bad")); ok {
		t.Fatal("stale cache schema must be rejected")
	}
}

func TestDisabledStoreIsNoOp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	s := Store{Dir: dir, Enabled: false}
	if err := s.Put("abc", runner.Result{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("disabled store created cache directory: %v", err)
	}
	if _, ok := s.Get("abc"); ok {
		t.Fatal("disabled store returned a hit")
	}
}

func TestEnabledStoreRejectsNonDigestKey(t *testing.T) {
	s := Store{Dir: t.TempDir(), Enabled: true}
	if err := s.Put("../escape", runner.Result{}); err == nil {
		t.Fatal("expected invalid-key rejection")
	}
	if _, ok := s.Get("../escape"); ok {
		t.Fatal("invalid key returned a hit")
	}
}

func TestConcurrentPutUsesIndependentTemporaryFiles(t *testing.T) {
	s := Store{Dir: t.TempDir(), Enabled: true}
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- s.Put(Key("same-key"), runner.Result{Verdict: model.VerdictSurvived})
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := s.Get(Key("same-key")); !ok {
		t.Fatal("missing cache entry after concurrent writes")
	}
}
