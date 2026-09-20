package literal

import "testing"

// Deliberately never calls Greeting(""), so DefaultName's mutant
// survives; only the "Hello, " prefix is exercised and killed.
func TestGreetingIncludesPrefix(t *testing.T) {
	got := Greeting("Ann")
	if got != "Hello, Ann" {
		t.Fatalf("Greeting(%q) = %q, want %q", "Ann", got, "Hello, Ann")
	}
}
