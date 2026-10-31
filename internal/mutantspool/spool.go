// Package mutantspool retains a bounded number of discovery candidates in
// memory and spills larger candidate streams to disk.
package mutantspool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/example/mutation-judge/internal/model"
)

const memoryBytes = 256 << 10
const memoryRecords = 64

type Spool struct {
	file   *os.File
	memory [][]byte
	bytes  int
	count  int
	closed bool
}

func New() (*Spool, error) { return &Spool{}, nil }

func (s *Spool) Len() int {
	if s == nil {
		return 0
	}
	return s.count
}

func (s *Spool) spill() error {
	f, err := os.CreateTemp("", "mutation-judge-mutants-*.jsonl")
	if err != nil {
		return fmt.Errorf("create mutation spool: %w", err)
	}
	for _, data := range s.memory {
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return fmt.Errorf("spill mutation spool: %w", err)
		}
	}
	s.file = f
	s.memory = nil
	s.bytes = 0
	return nil
}

func (s *Spool) Append(m model.Mutation) error {
	if s == nil || s.closed {
		return errors.New("mutation spool is closed")
	}
	if s.file != nil {
		if err := json.NewEncoder(s.file).Encode(m); err != nil {
			return fmt.Errorf("append mutation: %w", err)
		}
		s.count++
		return nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("append mutation: %w", err)
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
		return fmt.Errorf("append mutation: %w", err)
	}
	s.count++
	return nil
}

type Reader struct {
	file      *os.File
	decoder   *json.Decoder
	memory    [][]byte
	index     int
	remaining int
	closed    bool
}

func (s *Spool) Reader() (*Reader, error) {
	if s == nil || s.closed {
		return nil, errors.New("mutation spool is closed")
	}
	if s.file == nil {
		return &Reader{memory: s.memory, remaining: s.count}, nil
	}
	f, err := os.Open(s.file.Name())
	if err != nil {
		return nil, fmt.Errorf("open mutation spool: %w", err)
	}
	return &Reader{file: f, decoder: json.NewDecoder(f), remaining: s.count}, nil
}

func (r *Reader) Next() (model.Mutation, error) {
	if r == nil || r.closed {
		return model.Mutation{}, errors.New("mutation reader is closed")
	}
	if r.remaining == 0 {
		if r.file == nil {
			return model.Mutation{}, io.EOF
		}
		var extra json.RawMessage
		err := r.decoder.Decode(&extra)
		if err == io.EOF {
			return model.Mutation{}, io.EOF
		}
		if err == nil {
			return model.Mutation{}, errors.New("mutation spool has unexpected extra record")
		}
		return model.Mutation{}, fmt.Errorf("mutation spool tail: %w", err)
	}
	var m model.Mutation
	if r.file == nil {
		if err := json.Unmarshal(r.memory[r.index], &m); err != nil {
			return model.Mutation{}, fmt.Errorf("read mutation: %w", err)
		}
		r.index++
	} else if err := r.decoder.Decode(&m); err != nil {
		return model.Mutation{}, fmt.Errorf("read mutation: %w", err)
	}
	r.remaining--
	return m, nil
}

func (r *Reader) Close() error {
	if r == nil || r.closed {
		return nil
	}
	r.closed = true
	r.memory = nil
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
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
	if rmErr := os.Remove(name); err == nil {
		err = rmErr
	}
	return err
}
