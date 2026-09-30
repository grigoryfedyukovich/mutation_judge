package assertions

// Field mimics a nullable database column: Valid says whether the column holds
// a usable value.
type Field struct{ Valid bool }

// Scan populates a Field. It never fails; the error return exists so a
// caller (and the test) can check it -- and, like the uuid test, stop
// there.
func Scan(n int) (Field, error) {
	var f Field
	f.Valid = n > 0
	return f, nil
}
