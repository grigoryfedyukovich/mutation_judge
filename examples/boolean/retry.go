package boolean

func ShouldRetry(err error, retryable func(error) bool) bool {
	return err != nil && retryable(err)
}
