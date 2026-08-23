package channelop

// NewQueue returns a channel buffered for up to 4 pending sends before
// a fifth blocks.
func NewQueue() chan int {
	return make(chan int, 4)
}
