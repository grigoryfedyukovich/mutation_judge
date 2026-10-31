// Package resultspool retains small result streams in bounded memory and spills
// larger streams to disk so report rendering never needs the entire result set.
package resultspool

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/example/mutation-judge/internal/model"
)

// Limit resident JSON, by both bytes and number of records. The byte cap
// includes only serialized payloads; individual records are processed once.
const memoryBytes = 256 << 10
const memoryRecords = 64

// Spool owns either a bounded buffer of serialized JSON records or a private
// JSONL temporary file. Close always releases the buffer and removes the file.
// It is not a durable history or cache and is not safe for concurrent use.
type Spool struct {
	file   *os.File
	memory [][]byte
	bytes  int
	count  int
	closed bool
}

func New() (*Spool, error) {
	return &Spool{}, nil
}

func (s *Spool) spill() error {
	f, err := os.CreateTemp("", "mutation-judge-results-*.jsonl")
	if err != nil {
		return fmt.Errorf("create result spool: %w", err)
	}
	for _, record := range s.memory {
		if _, err := f.Write(record); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return fmt.Errorf("spill result spool: %w", err)
		}
	}
	s.file = f
	// The buffer is never needed after spilling. Clear references promptly.
	s.memory = nil
	s.bytes = 0
	return nil
}

func (s *Spool) Append(r model.Result) error {
	if s == nil || s.closed {
		return fmt.Errorf("result spool is closed")
	}
	// After the first spill, use the original streaming encoder; large
	// runs do not need an additional serialized buffer per record.
	if s.file != nil {
		if err := json.NewEncoder(s.file).Encode(r); err != nil {
			return fmt.Errorf("write result spool: %w", err)
		}
		s.count++
		return nil
	}
	// Keep serialized records so later caller mutations cannot affect them.
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode result spool: %w", err)
	}
	data = append(data, '\n')
	if s.file == nil && len(s.memory) < memoryRecords && len(data) <= memoryBytes-s.bytes {
		s.memory = append(s.memory, data)
		s.bytes += len(data)
		s.count++
		return nil
	}
	if s.file == nil {
		if err := s.spill(); err != nil {
			return err
		}
	}
	if _, err := s.file.Write(data); err != nil {
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

// ForEach makes repeatable ordered passes, including after an in-memory to
// disk transition. Errors from the consumer are propagated without swallowing.
func (s *Spool) ForEach(fn func(model.Result) error) error {
	if s == nil || s.closed {
		return fmt.Errorf("result spool is closed")
	}
	if s.file == nil {
		for i, data := range s.memory {
			var r model.Result
			if err := json.Unmarshal(data, &r); err != nil {
				return fmt.Errorf("read result %d: %w", i, err)
			}
			if err := fn(r); err != nil {
				return err
			}
		}
		return nil
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
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	s.memory = nil
	s.bytes = 0
	if s.file == nil {
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
