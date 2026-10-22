package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestR04CanceledSnapshotAndVerification(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package p"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cleanup, err := CopyModuleContext(ctx, root, "")
	if cleanup != nil {
		cleanup()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copy cancellation: %v", err)
	}
	if err := VerifyModuleCopyContext(ctx, root, root, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("verify cancellation: %v", err)
	}
}
