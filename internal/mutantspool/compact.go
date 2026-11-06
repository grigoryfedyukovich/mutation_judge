package mutantspool

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/example/mutation-judge/internal/model"
)

// Compact records are private to ephemeral discovery-sort runs. Each record
// has a uvarint byte length followed by fixed-order varint/string fields.
// Length framing permits detecting truncated/corrupt records without silently
// returning an incorrect mutation. Nothing in this format is persisted.
const maxCompactRecord = 512 << 20

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
	// Once decoded, the mutation owns its strings independently of its source.
	return decodeCompactString(string(data))
}

// decodeCompactString slices a single immutable allocation into the ten
// mutation strings. No individual field needs a new allocation, and it is
// safe for the caller to retain the result after the reader advances.
func decodeCompactString(blob string) (model.Mutation, error) {
	var m model.Mutation
	pos := 0
	nextString := func() (string, error) {
		n, count := binary.Uvarint([]byte(blob[pos:]))
		if count <= 0 || n > uint64(len(blob)-pos-count) {
			return "", errors.New("truncated compact mutation string")
		}
		pos += count
		s := blob[pos : pos+int(n)]
		pos += int(n)
		return s, nil
	}
	nextInt := func() (int, error) {
		v, count := binary.Varint([]byte(blob[pos:]))
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
	if pos != len(blob) {
		return model.Mutation{}, errors.New("compact mutation record has trailing data")
	}
	return m, nil
}

// readCompact consumes framed bytes directly into their final owned string
// allocation. Using a builder and Peek avoids the temporary read buffer plus
// a second whole-record copy while retaining ownership across future reads.
func readCompact(r *bufio.Reader) (model.Mutation, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return model.Mutation{}, err
	}
	if n > maxCompactRecord || n > uint64(math.MaxInt)-binary.MaxVarintLen64 {
		return model.Mutation{}, fmt.Errorf("compact mutation record too large: %d bytes", n)
	}
	// Most records fit in the buffered reader. Copy directly from Peek's
	// borrowed bytes into a single owned string, without a builder or scratch.
	if n <= 32<<10 {
		chunk, err := r.Peek(int(n))
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return model.Mutation{}, fmt.Errorf("read compact mutation record: %w", err)
		}
		blob := string(chunk)
		_, _ = r.Discard(int(n))
		return decodeCompactString(blob)
	}
	var owned strings.Builder
	owned.Grow(int(n))
	left := int(n)
	for left > 0 {
		chunkSize := min(left, 32<<10) // smaller than the 64 KiB reader buffer
		chunk, err := r.Peek(chunkSize)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return model.Mutation{}, fmt.Errorf("read compact mutation record: %w", err)
		}
		_, _ = owned.Write(chunk) // Grow above guarantees capacity.
		_, _ = r.Discard(len(chunk))
		left -= len(chunk)
	}
	return decodeCompactString(owned.String())
}
