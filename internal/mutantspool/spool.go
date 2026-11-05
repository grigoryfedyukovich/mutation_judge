// Package mutantspool retains a bounded number of discovery candidates in
// memory and spills larger candidate streams to disk.
package mutantspool

import (
	"bufio"
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
	file    *os.File
	writer  *bufio.Writer
	compact bool
	final   bool // private final discovery spool (distinct from intermediate merge runs)
	memory  [][]byte
	bytes   int
	count   int
	closed  bool
}

func New() (*Spool, error) { return &Spool{}, nil }

// NewCompact creates a spool for private, temporary discovery merge runs.
// The compact format is not a persisted interchange format and must never be
// cached or exposed as a public report.
func NewCompact() (*Spool, error) { return &Spool{compact: true}, nil }

// NewFinalCompact creates the private final mutation-discovery spool in the
// same version-local compact format as intermediate runs. Discovery produces
// Mutation values (including generated diffs), so the standard Reader can
// reconstruct every field. New() remains JSON-backed for existing callers.
// Neither format is a persistent cache or external report interchange format.
func NewFinalCompact() (*Spool, error) { return &Spool{compact: true, final: true}, nil }

func (s *Spool) Len() int {
	if s == nil {
		return 0
	}
	return s.count
}

func (s *Spool) spill() error {
	suffix := "mutation-judge-mutants-*.jsonl"
	if s.compact {
		suffix = "mutation-judge-merge-*.bin"
		if s.final {
			suffix = "mutation-judge-mutants-*.bin"
		}
	}
	f, err := os.CreateTemp("", suffix)
	if err != nil {
		return fmt.Errorf("create mutation spool: %w", err)
	}
	writer := bufio.NewWriterSize(f, 64<<10)
	for _, data := range s.memory {
		if _, err := writer.Write(data); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return fmt.Errorf("spill mutation spool: %w", err)
		}
	}
	s.file = f
	s.writer = writer
	s.memory = nil
	s.bytes = 0
	return nil
}

func (s *Spool) Append(m model.Mutation) error {
	if s == nil || s.closed {
		return errors.New("mutation spool is closed")
	}
	var data []byte
	var err error
	if s.compact {
		data = encodeCompact(m)
	} else {
		data, err = json.Marshal(m)
		if err != nil {
			return fmt.Errorf("append mutation: %w", err)
		}
		data = append(data, '\n')
	}
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
	if _, err := s.writer.Write(data); err != nil {
		return fmt.Errorf("append mutation: %w", err)
	}
	s.count++
	return nil
}

type Reader struct {
	file          *os.File
	decoder       *json.Decoder
	compactReader *bufio.Reader
	compact       bool
	memory        [][]byte
	index         int
	remaining     int
	rawScratch    []byte
	closed        bool
}

func (s *Spool) Reader() (*Reader, error) {
	if s == nil || s.closed {
		return nil, errors.New("mutation spool is closed")
	}
	if s.file == nil {
		return &Reader{memory: s.memory, compact: s.compact, remaining: s.count}, nil
	}
	// A reader may be opened before the spool is closed. Flush pending data
	// first so every accepted record is visible to independent readers.
	if err := s.writer.Flush(); err != nil {
		return nil, fmt.Errorf("flush mutation spool: %w", err)
	}
	f, err := os.Open(s.file.Name())
	if err != nil {
		return nil, fmt.Errorf("open mutation spool: %w", err)
	}
	r := &Reader{file: f, compact: s.compact, remaining: s.count}
	if s.compact {
		r.compactReader = bufio.NewReaderSize(f, 64<<10)
	} else {
		r.decoder = json.NewDecoder(f)
	}
	return r, nil
}

func (r *Reader) Next() (model.Mutation, error) {
	if r == nil || r.closed {
		return model.Mutation{}, errors.New("mutation reader is closed")
	}
	if r.remaining == 0 {
		if r.file == nil {
			return model.Mutation{}, io.EOF
		}
		if r.compact {
			if _, err := r.compactReader.Peek(1); err == io.EOF {
				return model.Mutation{}, io.EOF
			} else if err != nil {
				return model.Mutation{}, fmt.Errorf("mutation spool tail: %w", err)
			}
			return model.Mutation{}, errors.New("mutation spool has unexpected extra record")
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
		var err error
		if r.compact {
			m, err = decodeCompact(r.memory[r.index])
		} else {
			err = json.Unmarshal(r.memory[r.index], &m)
		}
		if err != nil {
			return model.Mutation{}, fmt.Errorf("read mutation: %w", err)
		}
		r.index++
	} else if r.compact {
		var err error
		m, err = readCompact(r.compactReader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return model.Mutation{}, fmt.Errorf("read mutation: %w", err)
		}
	} else if err := r.decoder.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
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
	flushErr := s.writer.Flush()
	err := s.file.Close()
	if flushErr != nil {
		err = flushErr
	}
	s.file = nil
	if rmErr := os.Remove(name); err == nil {
		err = rmErr
	}
	return err
}
