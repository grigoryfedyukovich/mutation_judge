package unrelated

import "testing"

// TestUnrelated checks the unrelated package remains runnable. Selection
// behavior is asserted through the recorded command, not sleep durations.
func TestUnrelated(t *testing.T) { Noop() }
