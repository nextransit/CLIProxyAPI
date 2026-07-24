package helps

import (
	"io"
	"time"
)

// idleTimeoutReader wraps an io.ReadCloser with an idle timeout.
// If no data is read within the timeout period, the underlying reader is closed
// and subsequent Read calls return an error. This prevents streaming reads from
// hanging indefinitely when the upstream stops sending data mid-stream without
// closing the connection.
//
// A single background goroutine monitors the timer and closes the connection
// on timeout, avoiding goroutine-per-read overhead.
type idleTimeoutReader struct {
	reader  io.ReadCloser
	timeout time.Duration
	timer   *time.Timer
	done    chan struct{}
}

// NewIdleTimeoutReadCloser wraps an io.ReadCloser with an idle timeout.
// The timeout resets on each successful Read call.
// If timeout is <= 0, the original reader is returned unchanged (no timeout).
func NewIdleTimeoutReadCloser(r io.ReadCloser, timeout time.Duration) io.ReadCloser {
	if timeout <= 0 || r == nil {
		return r
	}
	w := &idleTimeoutReader{
		reader:  r,
		timeout: timeout,
		timer:   time.NewTimer(timeout),
		done:    make(chan struct{}),
	}
	go w.watch()
	return w
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	// Reset the timer on each read attempt
	r.timer.Reset(r.timeout)
	n, err := r.reader.Read(p)
	// After a successful read, reset the timer for the next read
	if err == nil {
		r.timer.Reset(r.timeout)
	}
	return n, err
}

func (r *idleTimeoutReader) Close() error {
	close(r.done)
	if !r.timer.Stop() {
		select {
		case <-r.timer.C:
		default:
		}
	}
	return r.reader.Close()
}

// watch is a background goroutine that closes the underlying reader when the
// idle timeout fires. This interrupts any blocked Read call and causes the
// scanner to return an error.
func (r *idleTimeoutReader) watch() {
	select {
	case <-r.timer.C:
		r.reader.Close()
	case <-r.done:
		// Reader was closed explicitly; nothing to do
	}
}
