package assignment

import "testing"

// Deliberately never calls Withdraw or Tick, so the assignment
// mutants inside them (-= replaced with +=, ++ replaced with --)
// survive; only Deposit's += is exercised and killed.
func TestDepositIncreasesBalance(t *testing.T) {
	l := &Ledger{}
	l.Deposit(5)
	l.Deposit(3)
	if l.Balance() != 8 {
		t.Fatalf("Balance() = %d, want 8", l.Balance())
	}
}
