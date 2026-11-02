package mutantspool

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/example/mutation-judge/internal/model"
)

// Compact records are private to ephemeral discovery-sort runs. Each record
// has a uvarint byte length followed by fixed-order varint/string fields.
// Length framing permits detecting truncated/corrupt records without silently
// returning an incorrect mutation. Nothing in this format is persisted.
const maxCompactRecord = 512 << 20
const maxPooledCompactRecord = 64 << 10

// The decoded mutation owns an immutable copy of its record strings, so this
// temporary read buffer can be reused immediately after decoding.
var compactReadBuffers = sync.Pool{New: func() any { return make([]byte, 0, 4096) }}

func encodeCompact(m model.Mutation) []byte {
	// Reserve the maximum varint prefix in one allocation. Once the payload
	// length is known, slide the bytes down to the actual prefix width.
	b := make([]byte, binary.MaxVarintLen64, len(m.ID)+len(m.Original)+len(m.Replacement)+len(m.Description)+len(m.Suggestion)+len(m.Diff)+len(m.EquivalentReason)+256)
	for _, s := range []string{m.ID, m.Operator, m.RuleID, m.Span.File} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	for _, v := range []int{m.Span.StartByte, m.Span.EndByte, m.Span.StartLine, m.Span.StartCol, m.Span.EndLine, m.Span.EndCol} {
		b = binary.AppendVarint(b, int64(v))
	}
	for _, s := range []string{m.Original, m.Replacement, m.Description, m.Suggestion, m.Diff, m.EquivalentReason} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	prefix := binary.PutUvarint(b, uint64(len(b)-binary.MaxVarintLen64))
	copy(b[prefix:], b[binary.MaxVarintLen64:])
	return b[:prefix+len(b)-binary.MaxVarintLen64]
}

func decodeCompact(record []byte) (model.Mutation, error) {
	var m model.Mutation
	n, prefix := binary.Uvarint(record)
	if prefix <= 0 || n != uint64(len(record)-prefix) {
		return m, errors.New("invalid compact mutation record length")
	}
	return decodeCompactBody(record[prefix:])
}

func decodeCompactBody(data []byte) (model.Mutation, error) {
	var m model.Mutation
	// One immutable allocation backs all strings in this record. Slices keep
	// their bytes alive until the merged mutation has been consumed, avoiding
	// ten independently allocated strings per deserialized candidate.
	blob := string(data)
	pos := 0
	nextString := func() (string, error) {
		n, count := binary.Uvarint(data[pos:])
		if count <= 0 || n > uint64(len(data)-pos-count) {
			return "", errors.New("truncated compact mutation string")
		}
		pos += count
		s := blob[pos : pos+int(n)]
		pos += int(n)
		return s, nil
	}
	nextInt := func() (int, error) {
		v, count := binary.Varint(data[pos:])
		if count <= 0 {
			return 0, errors.New("truncated compact mutation integer")
		}
		if int64(int(v)) != v {
			return 0, errors.New("compact mutation integer overflow")
		}
		pos += count
		return int(v), nil
	}
	var err error
	for _, dst := range []*string{&m.ID, &m.Operator, &m.RuleID, &m.Span.File} {
		if *dst, err = nextString(); err != nil {
			return model.Mutation{}, err
		}
	}
	for _, dst := range []*int{&m.Span.StartByte, &m.Span.EndByte, &m.Span.StartLine, &m.Span.StartCol, &m.Span.EndLine, &m.Span.EndCol} {
		if *dst, err = nextInt(); err != nil {
			return model.Mutation{}, err
		}
	}
	for _, dst := range []*string{&m.Original, &m.Replacement, &m.Description, &m.Suggestion, &m.Diff, &m.EquivalentReason} {
		if *dst, err = nextString(); err != nil {
			return model.Mutation{}, err
		}
	}
	if pos != len(data) {
		return model.Mutation{}, errors.New("compact mutation record has trailing data")
	}
	return m, nil
}

func readCompact(r *bufio.Reader) (model.Mutation, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return model.Mutation{}, err
	}
	if n > maxCompactRecord || n > uint64(math.MaxInt)-binary.MaxVarintLen64 {
		return model.Mutation{}, fmt.Errorf("compact mutation record too large: %d bytes", n)
	}
	record := compactReadBuffers.Get().([]byte)
	if cap(record) < int(n) {
		record = make([]byte, int(n))
	} else {
		record = record[:int(n)]
	}
	_, err = io.ReadFull(r, record)
	var m model.Mutation
	if err == nil {
		m, err = decodeCompactBody(record)
	} else {
		err = fmt.Errorf("truncated compact mutation record: %w", err)
	}
	if cap(record) <= maxPooledCompactRecord {
		compactReadBuffers.Put(record[:0])
	}
	return m, err
}
