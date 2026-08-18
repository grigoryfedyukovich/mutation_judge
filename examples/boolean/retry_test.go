package boolean

import (
	"errors"
	"testing"
)

func TestPermanentFailure(t *testing.T) {
	err := errors.New("permanent")
	if ShouldRetry(err, func(error) bool { return false }) {
		t.Fatal("permanent errors must not be retried")
	}
}

func TestNilFailure(t *testing.T) {
	called := false
	if ShouldRetry(nil, func(error) bool { called = true; return true }) {
		t.Fatal("nil is not an error")
	}
	if called {
		t.Fatal("retryability callback must short-circuit for nil")
	}
}
