# Channel buffer mutation

`NewQueue` returns a channel buffered for 4 items. `TestNewQueueBuffersFour` sends exactly 4 values with no concurrent receiver, using `select`/`default` on each send so the test fails fast instead of blocking if that assumption is wrong. The `channel` operator replaces the buffer size with `0`; under that mutant the very first send has no room and hits `default` immediately, so the test fails deterministically rather than hanging.

```bash
./bin/mutation-judge --no-cache --operators channel ./examples/channel
```
