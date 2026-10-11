package cache

import (
	"context"
	"encoding/json"
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

func TestOnlyCompletedStableVerdictsAreCacheable(t *testing.T) {
	store := Store{Dir: t.TempDir(), Enabled: true}
	for _, tc := range []struct {
		verdict model.Verdict
		want    bool
	}{
		{model.VerdictKilled, true},
		{model.VerdictSurvived, true},
		{model.VerdictInvalid, true},
		{model.VerdictUnknown, false},
		{model.VerdictTimeout, false},
		{model.VerdictUnsupported, false},
		{model.VerdictEquivalent, false},
		{"", false},
	} {
		t.Run(string(tc.verdict), func(t *testing.T) {
			key := Key("verdict", string(tc.verdict))
			r := runner.Result{Verdict: tc.verdict}
			if got := Cacheable(r); got != tc.want {
				t.Fatalf("Cacheable(%s)=%v want %v", tc.verdict, got, tc.want)
			}
			if err := store.Put(key, r); err != nil {
				t.Fatal(err)
			}
			_, ok := store.Get(key)
			if ok != tc.want {
				t.Fatalf("cache hit for %q: %v, want %v", tc.verdict, ok, tc.want)
			}
		})
	}
}

func TestGetRejectsLegacyAndInjectedTransientEntries(t *testing.T) {
	store := Store{Dir: t.TempDir(), Enabled: true}
	for _, tc := range []struct {
		name   string
		schema string
		result runner.Result
	}{
		{"legacy-unknown", "mutation-judge-cache/v1", runner.Result{Verdict: model.VerdictUnknown}},
		{"legacy-killed", "mutation-judge-cache/v1", runner.Result{Verdict: model.VerdictKilled}},
		{"current-unknown", cacheSchema, runner.Result{Verdict: model.VerdictUnknown}},
		{"current-timeout", cacheSchema, runner.Result{Verdict: model.VerdictTimeout}},
		{"current-empty", cacheSchema, runner.Result{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := Key(tc.name)
			data, err := json.Marshal(Entry{Schema: tc.schema, Result: tc.result})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.Dir, key+".json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, ok := store.Get(key); ok {
				t.Fatal("transient or legacy entry must not be reusable")
			}
		})
	}
}

func TestPutContextDoesNotPersistCanceledResults(t *testing.T) {
	store := Store{Dir: t.TempDir(), Enabled: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	key := Key("canceled-but-killed")
	if err := store.PutContext(ctx, key, runner.Result{Verdict: model.VerdictKilled}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir, key+".json")); !os.IsNotExist(err) {
		t.Fatalf("canceled execution left cache file: %v", err)
	}
}
