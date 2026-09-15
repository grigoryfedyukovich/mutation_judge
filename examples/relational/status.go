package relational

// Status represents a job's lifecycle state.
type Status int

const (
	Pending Status = iota
	Running
	Done
)

// IsPending reports whether s is exactly Pending.
func IsPending(s Status) bool {
	return s == Pending
}

// IsFinished reports whether s is not Running -- either not yet
// started, or already done.
func IsFinished(s Status) bool {
	return s != Running
}
