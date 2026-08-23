package channelop

import "testing"

// Sends exactly the queue's buffered capacity without a concurrent
// receiver. Each send uses select-with-default specifically so this
// test fails fast instead of blocking if the channel turns out not to
// be buffered -- this only completes without hitting default because
// the channel really is buffered for 4 items.
func TestNewQueueBuffersFour(t *testing.T) {
	q := NewQueue()
	for i := 0; i < 4; i++ {
		select {
		case q <- i:
		default:
			t.Fatalf("send %d blocked; queue is not buffered for 4", i)
		}
	}
}
