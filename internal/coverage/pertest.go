package coverage

import "sort"

// PerTest holds one baseline coverage Map per top-level test function,
// built by running each test individually with its own coverage
// profile (see runner.ListTests and the orchestration in
// internal/analysis). It answers "which of these individually-run
// tests reached any line in this span", reusing Map.Covered entirely
// unchanged rather than reimplementing suffix matching per test --
// each test's own Map already handles the import-path-prefixed file
// keys a coverage profile reports exactly the way baseline coverage
// does.
type PerTest struct {
	names []string
	maps  []Map
}

// Add records one test's own coverage map. Safe to call repeatedly;
// PerTest has no other state to keep consistent.
func (p *PerTest) Add(name string, m Map) {
	p.names = append(p.names, name)
	p.maps = append(p.maps, m)
}

// Len reports how many tests have recorded coverage.
func (p *PerTest) Len() int {
	if p == nil {
		return 0
	}
	return len(p.names)
}

// CoveringTests returns the sorted, de-duplicated names of every test
// whose own individual run covered at least one line in [start, end]
// of file, and whether that determination is meaningful at all (false
// only when no test data has been recorded -- e.g. coverage-test
// selection is off, or building it failed outright; see the caller in
// internal/analysis for what happens then). An empty, non-nil result
// with known=true is a real, confident answer -- "profiled, and truly
// none of these tests reach this span" -- not a signal to keep
// searching; callers must still never narrow test execution down to
// an empty set (see mutantTestRun's doc comment for why).
func (p *PerTest) CoveringTests(file string, start, end int) (tests []string, known bool) {
	if p.Len() == 0 {
		return nil, false
	}
	seen := map[string]bool{}
	for i, m := range p.maps {
		if covered, ok := m.Covered(file, start, end); ok && covered && !seen[p.names[i]] {
			seen[p.names[i]] = true
			tests = append(tests, p.names[i])
		}
	}
	sort.Strings(tests)
	return tests, true
}
