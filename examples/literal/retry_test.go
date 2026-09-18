package literal

import (
	"errors"
	"testing"
)

// Deliberately never checks NewBuffer's actual length, so BufferSize's
// mutants survive; only MaxRetries' effect on Retry is exercised and
// killed. The expected count is hardcoded, not referenced via
// MaxRetries itself -- comparing against the same identifier the
// mutation changes would make the assertion track the mutant instead
// of catching it.
func TestRetryStopsAfterThreeAttempts(t *testing.T) {
	calls := 0
	err := Retry(func() error {
		calls++
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}
