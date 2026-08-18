package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func decodePackages(data []byte) ([]Package, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var out []Package
	for {
		var p Package
		err := dec.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}
