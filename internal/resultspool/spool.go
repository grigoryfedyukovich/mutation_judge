// Package resultspool stores ordered mutation results on disk so report
// rendering does not require retaining all completed results in heap memory.
package resultspool

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/example/mutation-judge/internal/model"
)

// Spool owns a private temporary file containing one JSON result per line.
// The file is removed by Close. It is not a durable history or cache.
type Spool struct {
	file  *os.File
	count int
}

func New() (*Spool, error) {
	f, err := os.CreateTemp("", "mutation-judge-results-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("create result spool: %w", err)
	}
	return &Spool{file: f}, nil
}

func (s *Spool) Append(r model.Result) error {
	if s == nil || s.file == nil {
		return fmt.Errorf("result spool is closed")
	}
	if err := json.NewEncoder(s.file).Encode(r); err != nil {
		return fmt.Errorf("write result spool: %w", err)
	}
	s.count++
	return nil
}

func (s *Spool) Len() int {
	if s == nil {
		return 0
	}
	return s.count
}

// ForEach scans sequentially from the start without buffering the entire file.
// A separate read descriptor allows rendering multiple passes safely.
func (s *Spool) ForEach(fn func(model.Result) error) error {
	if s == nil || s.file == nil {
		return fmt.Errorf("result spool is closed")
	}
	f, err := os.Open(s.file.Name())
	if err != nil {
		return fmt.Errorf("read result spool: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for i := 0; i < s.count; i++ {
		var r model.Result
		if err := dec.Decode(&r); err != nil {
			return fmt.Errorf("read result %d: %w", i, err)
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("result spool has unexpected extra record")
		}
		return fmt.Errorf("result spool tail: %w", err)
	}
	return nil
}

func (s *Spool) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	name := s.file.Name()
	err := s.file.Close()
	s.file = nil
	if removeErr := os.Remove(name); err == nil {
		err = removeErr
	}
	return err
}
