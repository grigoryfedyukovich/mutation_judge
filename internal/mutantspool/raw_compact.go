package mutantspool

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/example/mutation-judge/internal/model"
)

// CompactRecord is a validated, private-format discovery record. Payload,
// ID and the strings within it are borrowed from the Reader: their contents
// are valid until the next call to that Reader. A merge must write or consume
// the record before advancing the originating Reader.
//
// This format is only for temporary discovery runs; public spools still use
// the JSON representation.
type CompactRecord struct {
	Payload   []byte
	ID        []byte
	StartByte int
}

// Mutation materializes the record once, when its final output is required.
func (rec CompactRecord) Mutation() (model.Mutation, error) {
	return decodeCompactBody(rec.Payload)
}

// compactKey validates the entire wire record without constructing strings.
// In particular, truncated or extra fields must fail in an intermediate merge
// rather than being copied silently to later passes.
func compactKey(data []byte) (CompactRecord, error) {
	var rec CompactRecord
	rec.Payload = data
	pos := 0
	nextString := func() ([]byte, error) {
		if pos >= len(data) {
			return nil, errors.New("truncated compact mutation string")
		}
		n, width := binary.Uvarint(data[pos:])
		if width <= 0 || n > uint64(len(data)-pos-width) {
			return nil, errors.New("truncated compact mutation string")
		}
		pos += width
		value := data[pos : pos+int(n)]
		pos += int(n)
		return value, nil
	}
	nextInt := func() (int, error) {
		if pos >= len(data) {
			return 0, errors.New("truncated compact mutation integer")
		}
		v, width := binary.Varint(data[pos:])
		if width <= 0 || int64(int(v)) != v {
			return 0, errors.New("invalid compact mutation integer")
		}
		pos += width
		return int(v), nil
	}
	for i := 0; i < 4; i++ {
		value, err := nextString()
		if err != nil {
			return CompactRecord{}, err
		}
		if i == 0 {
			rec.ID = value
		}
	}
	for i := 0; i < 6; i++ {
		value, err := nextInt()
		if err != nil {
			return CompactRecord{}, err
		}
		if i == 0 {
			rec.StartByte = value
		}
	}
	for i := 0; i < 6; i++ {
		if _, err := nextString(); err != nil {
			return CompactRecord{}, err
		}
	}
	if pos != len(data) {
		return CompactRecord{}, errors.New("compact mutation record has trailing data")
	}
	return rec, nil
}

// NextCompactRecord returns a borrowed, validated compact record without
// allocating one string for each record or decoding into a full Mutation.
func (r *Reader) NextCompactRecord() (CompactRecord, error) {
	if r == nil || r.closed || !r.compact {
		return CompactRecord{}, errors.New("compact mutation reader is unavailable")
	}
	if r.remaining == 0 {
		if r.file != nil {
			if _, err := r.compactReader.Peek(1); err == io.EOF {
				return CompactRecord{}, io.EOF
			} else if err != nil {
				return CompactRecord{}, fmt.Errorf("mutation spool tail: %w", err)
			}
			return CompactRecord{}, errors.New("mutation spool has unexpected extra record")
		}
		return CompactRecord{}, io.EOF
	}
	var payload []byte
	if r.file == nil {
		record := r.memory[r.index]
		n, width := binary.Uvarint(record)
		if width <= 0 || n != uint64(len(record)-width) || n > maxCompactRecord {
			return CompactRecord{}, errors.New("invalid compact mutation record length")
		}
		payload = record[width:]
	} else {
		n, err := binary.ReadUvarint(r.compactReader)
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return CompactRecord{}, fmt.Errorf("read compact length: %w", err)
		}
		if n > maxCompactRecord || n > uint64(math.MaxInt) {
			return CompactRecord{}, fmt.Errorf("compact mutation record too large: %d bytes", n)
		}
		if cap(r.rawScratch) < int(n) {
			r.rawScratch = make([]byte, int(n))
		}
		payload = r.rawScratch[:int(n)]
		if _, err := io.ReadFull(r.compactReader, payload); err != nil {
			return CompactRecord{}, fmt.Errorf("truncated compact mutation record: %w", err)
		}
	}
	rec, err := compactKey(payload)
	if err != nil {
		return CompactRecord{}, fmt.Errorf("read compact mutation: %w", err)
	}
	r.remaining--
	r.index++
	return rec, nil
}

// AppendCompactRecord forwards a validated record without encoding the
// Mutation again. Its payload is consumed synchronously (never retained by
// reference), so the caller can safely advance its Reader immediately.
func (s *Spool) AppendCompactRecord(rec CompactRecord) error {
	if s == nil || s.closed || !s.compact {
		return errors.New("compact mutation spool is unavailable")
	}
	if len(rec.Payload) > maxCompactRecord {
		return fmt.Errorf("compact mutation record too large: %d bytes", len(rec.Payload))
	}
	// Keep the same fail-closed guarantees as normal compact serialization.
	if _, err := compactKey(rec.Payload); err != nil {
		return err
	}
	var header [binary.MaxVarintLen64]byte
	prefixLen := binary.PutUvarint(header[:], uint64(len(rec.Payload)))
	length := prefixLen + len(rec.Payload)
	if s.file == nil && len(s.memory) < memoryRecords && length <= memoryBytes-s.bytes {
		data := make([]byte, length)
		copy(data, header[:prefixLen])
		copy(data[prefixLen:], rec.Payload)
		s.memory = append(s.memory, data)
		s.bytes += length
		s.count++
		return nil
	}
	if s.file == nil {
		if err := s.spill(); err != nil {
			return err
		}
	}
	if _, err := s.writer.Write(header[:prefixLen]); err != nil {
		return fmt.Errorf("append compact record: %w", err)
	}
	if _, err := s.writer.Write(rec.Payload); err != nil {
		return fmt.Errorf("append compact record: %w", err)
	}
	s.count++
	return nil
}
