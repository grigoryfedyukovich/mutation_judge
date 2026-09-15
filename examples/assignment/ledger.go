package assignment

// Ledger tracks a running balance.
type Ledger struct {
	balance int
}

// Deposit increases the balance by amount.
func (l *Ledger) Deposit(amount int) {
	l.balance += amount
}

// Withdraw decreases the balance by amount.
func (l *Ledger) Withdraw(amount int) {
	l.balance -= amount
}

// Tick increases the balance by exactly one, used for a simple visit
// counter elsewhere in this package's real-world analogue.
func (l *Ledger) Tick() {
	l.balance++
}

// Balance returns the current balance.
func (l *Ledger) Balance() int {
	return l.balance
}
