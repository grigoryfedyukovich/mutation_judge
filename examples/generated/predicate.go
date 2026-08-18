package generated

// Enabled is ordinary source and is always eligible for mutation.
func Enabled(primary, fallback bool) bool {
	return primary || fallback
}
