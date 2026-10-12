package coverage

import "sort"

// PerTest holds one baseline coverage Map per package-qualified top-level test,
// built by running each test individually with its own coverage
// profile (see runner.ListTests and the orchestration in
// internal/analysis). It answers "which of these individually-run
// tests reached any line in this span", reusing Map.Covered entirely
// unchanged rather than reimplementing suffix matching per test --
// each test's own Map already handles the import-path-prefixed file
// keys a coverage profile reports exactly the way baseline coverage
// does.
type PerTest struct {
	names    []string
	packages []string
	maps     []Map
	// Examples and fuzz targets execute in ordinary go test, but do not have
	// individual coverage profiles. Preserve these in every narrowed -run.
	alwaysRun map[string][]string // test package -> runnable names
}

// Add records one test's own coverage map. Safe to call repeatedly;
// PerTest has no other state to keep consistent.
func (p *PerTest) Add(name string, m Map) {
	p.AddInPackage("", name, m)
}

// AddInPackage preserves the owning test package. Top-level test names need
// not be unique across packages; they are not evidence identities by themselves.
func (p *PerTest) AddInPackage(pkg, name string, m Map) {
	p.packages = append(p.packages, pkg)
	p.names = append(p.names, name)
	p.maps = append(p.maps, m)
}

// AddAlwaysRunInPackage records a runnable example or fuzz target that cannot
// be excluded based on ordinary Test-function coverage alone.
func (p *PerTest) AddAlwaysRunInPackage(pkg, name string) {
	if p.alwaysRun == nil {
		p.alwaysRun = make(map[string][]string)
	}
	p.alwaysRun[pkg] = append(p.alwaysRun[pkg], name)
}

// AlwaysRunForPackages returns the examples and fuzz seed targets that must
// remain enabled for any potentially observing test package. A name collision
// across packages is harmless: -run may conservatively execute extra units.
func (p *PerTest) AlwaysRunForPackages(possible []string) []string {
	seen := map[string]bool{}
	var names []string
	for _, pkg := range possible {
		for _, name := range p.alwaysRun[pkg] {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// CoveringTestsForPackages is the conservative selector for mutation runs.
// possible contains every test package that could transitively observe the
// mutated package (from go list -deps -test). For each such test we require
// an instrumented, unambiguous target file; otherwise an absent profile row
// could mean missing instrumentation, not an uncovered span, so we fall back.
// Names are returned for go test -run; duplicates across test packages are
// deliberately retained by the underlying package-qualified records before
// conversion to a regex (which may conservatively run more tests).
func (p *PerTest) CoveringTestsForPackages(file string, start, end int, possible []string) ([]string, bool) {
	if p.Len() == 0 || len(possible) == 0 {
		return nil, false
	}
	allowed := make(map[string]bool, len(possible))
	for _, pkg := range possible {
		allowed[pkg] = true
	}
	covered := make(map[string]bool)
	saw := make(map[string]bool)
	for i, m := range p.maps {
		if !allowed[p.packages[i]] {
			continue
		}
		saw[p.packages[i]] = true
		yes, known := m.Covered(file, start, end)
		if !known { // missing/ambiguous cross-package instrumentation
			return nil, false
		}
		if yes {
			covered[p.names[i]] = true
		}
	}
	// A dependent package with its own tests but no successfully recorded
	// test coverage must not silently disappear from the observing universe.
	for _, pkg := range possible {
		if !saw[pkg] {
			return nil, false
		}
	}
	names := make([]string, 0, len(covered))
	for name := range covered {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, true
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
