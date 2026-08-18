package arithmetic

import "testing"

func TestTotal(t *testing.T) {
	if got := Total(10, 3); got != 13 {
		t.Fatalf("Total(10, 3) = %d, want 13", got)
	}
}

func TestProduct(t *testing.T) {
	if got := Product(6, 4); got != 24 {
		t.Fatalf("Product(6, 4) = %d, want 24", got)
	}
}

func TestGreeting(t *testing.T) {
	if got := Greeting("Ada"); got != "hello, Ada" {
		t.Fatalf("Greeting(Ada) = %q, want %q", got, "hello, Ada")
	}
}
