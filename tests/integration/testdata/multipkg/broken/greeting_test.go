package broken

import "testing"

func TestGreeting(t *testing.T) {
	if got := Greeting("Ada"); got != "hello, Ada" {
		t.Fatalf("Greeting(Ada) = %q, want %q", got, "hello, Ada")
	}
}
