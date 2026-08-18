package generated

import "testing"

func TestEnabledFallback(t *testing.T) {
	if !Enabled(false, true) {
		t.Fatal("fallback should enable the feature")
	}
}

func TestEnabledPrimary(t *testing.T) {
	if !Enabled(true, false) {
		t.Fatal("primary should enable the feature")
	}
}

func TestGeneratedEnabledFallback(t *testing.T) {
	if !GeneratedEnabled(false, true) {
		t.Fatal("generated fallback should enable the feature")
	}
}

func TestGeneratedEnabledPrimary(t *testing.T) {
	if !GeneratedEnabled(true, false) {
		t.Fatal("generated primary should enable the feature")
	}
}
