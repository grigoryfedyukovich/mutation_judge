package equivalentop

// Item is compared lexicographically by Priority, then by
// SubmittedAt as a tie-breaker.
type Item struct {
	Priority    int
	SubmittedAt int
}

// Less reports whether a sorts before b. The Priority comparison is
// guarded by an explicit != check, so its boundary mutant is provably
// equivalent (see this example's README); the SubmittedAt comparison
// is not guarded by anything and is an ordinary boundary mutant.
func Less(a, b Item) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	return a.SubmittedAt < b.SubmittedAt
}
