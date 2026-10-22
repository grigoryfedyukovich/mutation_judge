package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Helper runs in a subprocess with ordinary Go test process semantics.
func TestProcessCaptureHelper(t *testing.T) {
	switch os.Getenv("MJ_CAPTURE_HELPER") {
	case "stdout-overflow":
		chunk := strings.Repeat("X", 64<<10)
		for i := 0; i <= MaxCaptureBytes/len(chunk); i++ {
			fmt.Print(chunk)
		}
	case "stderr-overflow":
		fmt.Fprint(os.Stderr, strings.Repeat("E", maxStderrBytes+512))
	case "combined-overflow":
		chunk := strings.Repeat("X", 64<<10)
		for i := 0; i <= MaxCaptureBytes/len(chunk); i++ {
			if i%2 == 0 {
				fmt.Fprint(os.Stderr, chunk)
			} else {
				fmt.Print(chunk)
			}
		}
	case "normal":
		fmt.Print("ok")
	case "fail":
		fmt.Fprint(os.Stderr, "error message")
		os.Exit(17)
	}
}

func helperCmd(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := Command(ctx, os.Args[0], "-test.run=^TestProcessCaptureHelper$")
	cmd.Env = append(os.Environ(), "MJ_CAPTURE_HELPER="+mode)
	return cmd
}

func TestOutputLimitsRejectInsteadOfTruncating(t *testing.T) {
	for _, mode := range []string{"stdout-overflow", "stderr-overflow", "combined-overflow"} {
		t.Run(mode, func(t *testing.T) {
			cmd := helperCmd(t, mode)
			var out []byte
			var err error
			if mode == "combined-overflow" {
				out, err = CombinedOutput(cmd)
			} else {
				out, err = Output(cmd)
			}
			if !errors.Is(err, ErrOutputLimit) {
				t.Fatalf("expected output limit, got %v", err)
			}
			if len(out) > MaxCaptureBytes {
				t.Fatalf("retained %d bytes", len(out))
			}
		})
	}
}

func TestOutputRetainsExitStderr(t *testing.T) {
	out, err := Output(helperCmd(t, "normal"))
	if err != nil || !strings.HasPrefix(string(out), "ok") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	_, err = Output(helperCmd(t, "fail"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 || !strings.Contains(string(exit.Stderr), "error message") {
		t.Fatalf("exit: %v", err)
	}
}
