package errorreturn

import "errors"

var errNotFound = errors.New("not found")

// Lookup finds a value by key, propagating any lookup error.
func Lookup(m map[string]int, key string) (int, error) {
	v, ok := m[key]
	err := checkFound(ok)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func checkFound(ok bool) error {
	if !ok {
		return errNotFound
	}
	return nil
}
