//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/mutation-judge/internal/config"
	"github.com/example/mutation-judge/internal/model"
	"github.com/example/mutation-judge/internal/runner"
)

func TestR04PreparationDeadlineBoundsToolchain(t *testing.T) {
	fakeDir := t.TempDir()
	fakeGo := filepath.Join(fakeDir, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nsleep 5\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := config.Default()
	c.PreparationTimeout = 80 * time.Millisecond
	before := time.Now()
	_, err := (Engine{}).Analyze(context.Background(), Request{CWD: fakeDir, Patterns: []string{"./..."}, Config: c})
	if err == nil {
		t.Fatal("expected preparation failure")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "killed") {
		t.Fatalf("unexpected deadline error: %v", err)
	}
	if elapsed := time.Since(before); elapsed > 3*time.Second {
		t.Fatalf("deadline returned after %v", elapsed)
	}
}

type r04SlowBackend struct{}

func (*r04SlowBackend) Run(ctx context.Context, req runner.Request) runner.Result {
	src, err := os.ReadFile(filepath.Join(req.Root, "p.go"))
	if err == nil && !strings.Contains(string(src), ">=") {
		return runner.Result{Verdict: model.VerdictSurvived}
	}
	<-ctx.Done()
	return runner.Result{Verdict: model.VerdictUnknown}
}

func TestR04TotalTimeoutMarksPartialReport(t *testing.T) {
	dir, _ := fourMutantFixture(t)
	cfg := config.Default()
	cfg.Cache = false
	cfg.Operators = []string{"boundary"}
	cfg.TotalTimeout = 1800 * time.Millisecond
	r, err := (Engine{Version: "test", Backend: &r04SlowBackend{}}).Analyze(context.Background(), Request{CWD: dir, Patterns: []string{"."}, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete {
		t.Fatal("overall deadline must not produce a complete report")
	}
	if exceeded, _ := r.Bounds["analysis_deadline_exceeded"].(bool); !exceeded {
		t.Fatalf("no deadline evidence: %v", r.Bounds)
	}
	if len(r.Warnings) == 0 || !strings.Contains(strings.Join(r.Warnings, " "), "deadline exceeded") {
		t.Fatalf("no timeout warning: %v", r.Warnings)
	}
}
