package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/example/mutation-judge/internal/model"
)

// These are *retained-memory* limits, not limits on how much output a test may
// produce. Stdout is consumed as a stream so even a noisy child cannot force
// the runner to retain its complete JSON output in RAM.
const (
	maxJSONEventBytes = 256 << 10
	maxOutputBytes    = 64 << 10
	maxStderrBytes    = 16 << 10
	maxEvidenceBytes  = 256 << 10
	maxEvidenceEvents = 4096
	maxTrackedTests   = 8192
	maxIdentityBytes  = 512
	maxAssertions     = 128
	maxPanics         = 128
)

type boundedText struct {
	data  []byte
	total uint64
	limit int
}

func (b *boundedText) Write(p []byte) (int, error) {
	n := len(p)
	b.total += uint64(n)
	if avail := b.limit - len(b.data); avail > 0 {
		if avail > len(p) {
			avail = len(p)
		}
		b.data = append(b.data, p[:avail]...)
	}
	return n, nil
}

func (b *boundedText) truncated() bool { return b.total > uint64(b.limit) }

// jsonEventStream implements io.Writer for exec.Cmd.Stdout. Each complete JSON
// record is processed immediately and discarded. Control events update bounded
// classification state independently of the small diagnostic event sample.
// A single malformed or oversized event makes negative classifications
// inconclusive; explicit observed test failures are still valid kills.
type jsonEventStream struct {
	line              []byte
	dropping          bool
	incomplete        bool
	eventCount        uint64
	display           boundedText
	evidence          []testEvent
	evidenceBytes     int
	evidenceTruncated bool
	timeoutHeader     bool
	started           map[string]bool
	failed            map[string]bool
	pkgRan            map[string]bool
	pkgBuildFailed    map[string]bool
}

func newJSONEventStream() *jsonEventStream {
	return &jsonEventStream{
		display: boundedText{limit: maxOutputBytes},
		started: make(map[string]bool), failed: make(map[string]bool),
		pkgRan: make(map[string]bool), pkgBuildFailed: make(map[string]bool),
	}
}

func (s *jsonEventStream) Write(p []byte) (int, error) {
	original := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.acceptBytes(p)
			break
		}
		s.acceptBytes(p[:i])
		if !s.dropping {
			s.consumeLine(s.line)
		}
		s.line = s.line[:0]
		s.dropping = false
		p = p[i+1:]
	}
	return original, nil
}

func (s *jsonEventStream) acceptBytes(p []byte) {
	if s.dropping {
		return
	}
	if len(s.line)+len(p) > maxJSONEventBytes {
		s.dropping = true
		s.incomplete = true
		s.line = s.line[:0]
		return
	}
	s.line = append(s.line, p...)
}

func (s *jsonEventStream) consumeLine(line []byte) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var e testEvent
	if err := json.Unmarshal(line, &e); err != nil {
		s.incomplete = true
		return
	}
	s.eventCount++
	if len(e.Package) > maxIdentityBytes || len(e.Test) > maxIdentityBytes {
		s.incomplete = true
		return
	}
	s.observe(e)
}

func (s *jsonEventStream) observe(e testEvent) {
	if e.Action == "output" {
		_, _ = s.display.Write([]byte(e.Output))
		if timeoutPanicRE.MatchString(e.Output) {
			s.timeoutHeader = true
		}
		if e.Test == "" && packageFailSummaryRE.MatchString(e.Output) {
			if _, exists := s.pkgBuildFailed[e.Package]; exists || len(s.pkgBuildFailed) < maxTrackedTests {
				s.pkgBuildFailed[e.Package] = true
			} else {
				s.incomplete = true
			}
		}
	}
	if e.Test != "" {
		key := e.Package + "\x00" + e.Test
		switch e.Action {
		case "run":
			if !s.track(s.started, key) {
				s.incomplete = true
			}
			if !s.track(s.pkgRan, e.Package) {
				s.incomplete = true
			}
		case "pass", "skip":
			delete(s.started, key)
		case "fail":
			delete(s.started, key)
			if !s.track(s.failed, key) {
				s.incomplete = true
			}
		}
	}
	// Save only small, relevant events for human-readable assertion and
	// panic extraction. The verdict never depends on this sample.
	if e.Test != "" && (e.Action == "run" || e.Action == "fail" || e.Action == "pass" || e.Action == "skip" || e.Action == "output") {
		bytesNeeded := len(e.Package) + len(e.Test) + len(e.Action) + len(e.Output) + 128
		if len(s.evidence) >= maxEvidenceEvents || bytesNeeded+s.evidenceBytes > maxEvidenceBytes {
			s.evidenceTruncated = true
			return
		}
		s.evidenceBytes += bytesNeeded
		s.evidence = append(s.evidence, e)
	}
}

func (s *jsonEventStream) track(m map[string]bool, key string) bool {
	if m[key] {
		return true
	}
	if len(m) >= maxTrackedTests {
		return false
	}
	m[key] = true
	return true
}

func (s *jsonEventStream) finish() {
	if len(bytes.TrimSpace(s.line)) > 0 || s.dropping {
		s.incomplete = true
	}
	s.line = nil
}

func (s *jsonEventStream) classify() (model.Verdict, []string) {
	names := make(map[string]bool)
	for key := range s.failed {
		_, name, _ := strings.Cut(key, "\x00")
		names[name] = true
	}
	for key := range s.started {
		_, name, _ := strings.Cut(key, "\x00")
		names[name] = true
	}
	if len(names) > 0 {
		tests := make([]string, 0, len(names))
		for name := range names {
			tests = append(tests, name)
		}
		sort.Strings(tests)
		return model.VerdictKilled, tests
	}
	if s.incomplete {
		return model.VerdictUnknown, nil
	}
	for pkg := range s.pkgBuildFailed {
		if !s.pkgRan[pkg] {
			return model.VerdictInvalid, nil
		}
	}
	return model.VerdictKilled, nil
}

// diagnosticOutput emits at most maxOutputBytes of evidence from the two
// streams combined, plus a fixed-size truncation annotation. Total byte count
// is measured as data arrives, without retaining the bytes themselves.
func (s *jsonEventStream) diagnosticOutput(stderr boundedText) string {
	budget := maxOutputBytes
	var prefix []byte
	if len(stderr.data) > budget {
		prefix = append(prefix, stderr.data[:budget]...)
	} else {
		prefix = append(prefix, stderr.data...)
		remain := budget - len(prefix)
		if remain > len(s.display.data) {
			remain = len(s.display.data)
		}
		prefix = append(prefix, s.display.data[:remain]...)
	}
	for len(prefix) > 0 && !utf8.Valid(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	out := strings.TrimSpace(string(prefix))
	total := stderr.total + s.display.total
	if total > uint64(budget) || s.incomplete || s.evidenceTruncated {
		out += fmt.Sprintf("\n... output/evidence truncated (%d output bytes observed)", total)
	}
	return strings.TrimSpace(out)
}

func (s *jsonEventStream) outputTruncated(stderr boundedText) bool {
	return stderr.truncated() || s.display.truncated() || s.evidenceTruncated || s.incomplete || stderr.total+s.display.total > maxOutputBytes
}

func limitAssertions(in []model.Assertion) []model.Assertion {
	if len(in) > maxAssertions {
		return in[:maxAssertions]
	}
	return in
}
func limitPanics(in []model.Panic) []model.Panic {
	if len(in) > maxPanics {
		return in[:maxPanics]
	}
	return in
}
