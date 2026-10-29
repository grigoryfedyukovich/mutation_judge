// Package mutantspool keeps discovery candidates on disk while mutation
// analysis consumes only a bounded number of candidates at a time.
package mutantspool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/example/mutation-judge/internal/model"
)

type Spool struct {
	file  *os.File
	count int
}

func New() (*Spool, error) {
	f, err := os.CreateTemp("", "mutation-judge-mutants-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("create mutation spool: %w", err)
	}
	return &Spool{file: f}, nil
}

func (s *Spool) Len() int {
	if s == nil {
		return 0
	}
	return s.count
}
func (s *Spool) Append(m model.Mutation) error {
	if s == nil || s.file == nil {
		return errors.New("mutation spool is closed")
	}
	if err := json.NewEncoder(s.file).Encode(m); err != nil {
		return fmt.Errorf("append mutation: %w", err)
	}
	s.count++
	return nil
}

type Reader struct {
	file      *os.File
	decoder   *json.Decoder
	remaining int
}

func (s *Spool) Reader() (*Reader, error) {
	if s == nil || s.file == nil {
		return nil, errors.New("mutation spool is closed")
	}
	f, err := os.Open(s.file.Name())
	if err != nil {
		return nil, fmt.Errorf("open mutation spool: %w", err)
	}
	return &Reader{file: f, decoder: json.NewDecoder(f), remaining: s.count}, nil
}

func (r *Reader) Next() (model.Mutation, error) {
	if r == nil || r.file == nil {
		return model.Mutation{}, errors.New("mutation reader is closed")
	}
	if r.remaining == 0 {
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
	if err := r.decoder.Decode(&m); err != nil {
		return model.Mutation{}, fmt.Errorf("read mutation: %w", err)
	}
	r.remaining--
	return m, nil
}

func (r *Reader) Close() error {
	if r == nil || r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (s *Spool) Close() error {
	if s == nil || s.file == nil {
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
