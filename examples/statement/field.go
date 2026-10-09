package statement

import "errors"

// Field mimics a nullable database column, the shape of google/uuid's
// NullUUID: Scan must set both the value and the Valid flag.
type Field struct {
	Value string
	Valid bool
}

// Scan stores src. An empty src is an error.
func (f *Field) Scan(src string) error {
	if src == "" {
		return errors.New("empty source")
	}
	f.Value = src
	f.Valid = true
	return nil
}
