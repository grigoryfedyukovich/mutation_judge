package literal

// MaxRetries caps how many times Retry attempts an operation.
const MaxRetries = 3

// Retry calls fn up to MaxRetries times, returning the first nil
// error, or the last error if every attempt fails.
func Retry(fn func() error) error {
	var err error
	for i := 0; i < MaxRetries; i++ {
		err = fn()
		if err == nil {
			return nil
		}
	}
	return err
}

// BufferSize is the capacity NewBuffer allocates.
const BufferSize = 8

// NewBuffer returns a zeroed byte slice of length BufferSize.
func NewBuffer() []byte {
	return make([]byte, BufferSize)
}
