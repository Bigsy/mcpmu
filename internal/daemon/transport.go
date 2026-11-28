package daemon

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

var (
	errOutboundBudgetExceeded = errors.New("daemon session outbound backlog exceeded its byte budget")
	errClientStalled          = errors.New("daemon session client accepted no bytes within the stall timeout")
)

// queuedWriter decouples a Session from its client socket. Write never blocks:
// it appends to a backlog that one goroutine drains to the socket, so a client
// that reads slowly cannot stall the shared Core, and a burst of responses
// waits in the backlog instead of ending the session. The session is
// disconnected only when the socket accepts no bytes for stallTimeout, or when
// the backlog grows past budget bytes.
type queuedWriter struct {
	conn         net.Conn
	done         <-chan struct{}
	cancel       context.CancelFunc
	exited       chan struct{}
	ready        chan struct{} // capacity 1: wakes run after the backlog grows
	budget       int
	stallTimeout time.Duration

	mu      sync.Mutex
	pending []queuedWrite
	queued  int  // bytes waiting or being written; guarded by mu
	failed  bool // guarded by mu
}

type queuedWrite struct {
	data    []byte
	flushed chan struct{}
}

func newQueuedWriter(conn net.Conn, budget int, stallTimeout time.Duration, done <-chan struct{}, cancel context.CancelFunc) *queuedWriter {
	writer := &queuedWriter{
		conn: conn, done: done, cancel: cancel,
		exited: make(chan struct{}), ready: make(chan struct{}, 1),
		budget: budget, stallTimeout: stallTimeout,
	}
	go writer.run()
	return writer
}

func (writer *queuedWriter) Write(data []byte) (int, error) {
	select {
	case <-writer.done:
		return 0, io.ErrClosedPipe
	default:
	}
	writer.mu.Lock()
	if writer.failed {
		writer.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	// An empty backlog accepts one frame of any size, so a single oversized
	// response is delivered rather than refused.
	if writer.queued > 0 && writer.queued+len(data) > writer.budget {
		writer.mu.Unlock()
		writer.fail(errOutboundBudgetExceeded)
		return 0, errOutboundBudgetExceeded
	}
	writer.pending = append(writer.pending, queuedWrite{data: append([]byte(nil), data...)})
	writer.queued += len(data)
	writer.mu.Unlock()
	writer.wake()
	return len(data), nil
}

// Flush waits until every write queued before the marker has reached the
// socket. It is used when a half-closed shim input lets a Session finish while
// its final responses are still buffered in the daemon writer.
func (writer *queuedWriter) Flush(ctx context.Context) error {
	flushed := make(chan struct{})
	writer.mu.Lock()
	if writer.failed {
		writer.mu.Unlock()
		return io.ErrClosedPipe
	}
	writer.pending = append(writer.pending, queuedWrite{flushed: flushed})
	writer.mu.Unlock()
	writer.wake()
	select {
	case <-flushed:
		return nil
	case <-writer.exited:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (writer *queuedWriter) wake() {
	select {
	case writer.ready <- struct{}{}:
	default:
	}
}

// fail ends the session once; later writes return io.ErrClosedPipe.
func (writer *queuedWriter) fail(err error) {
	writer.mu.Lock()
	already := writer.failed
	writer.failed = true
	writer.pending = nil
	writer.mu.Unlock()
	if already {
		return
	}
	select {
	case <-writer.done: // the session is already ending; the error is teardown noise
	default:
		log.Printf("Disconnecting daemon session: %v", err)
	}
	writer.cancel()
	_ = writer.conn.Close()
}

func (writer *queuedWriter) run() {
	defer close(writer.exited)
	for {
		select {
		case <-writer.ready:
		case <-writer.done:
			return
		}
		writer.mu.Lock()
		batch := writer.pending
		writer.pending = nil
		writer.mu.Unlock()
		for i, item := range batch {
			batch[i] = queuedWrite{} // let a written frame be collected
			if item.flushed != nil {
				close(item.flushed)
				continue
			}
			err := writer.writeFrame(item.data)
			writer.mu.Lock()
			writer.queued -= len(item.data)
			writer.mu.Unlock()
			if err != nil {
				writer.fail(err)
				return
			}
			select {
			case <-writer.done:
				return
			default:
			}
		}
	}
}

// writeFrame writes one frame. Each write that makes progress restarts the
// stall interval, so a slow reader can still finish a large frame; only a
// socket that accepts no bytes for stallTimeout ends the session.
func (writer *queuedWriter) writeFrame(data []byte) error {
	for len(data) > 0 {
		if err := writer.conn.SetWriteDeadline(time.Now().Add(writer.stallTimeout)); err != nil {
			return err
		}
		written, err := writer.conn.Write(data)
		data = data[written:]
		switch {
		case err == nil && written == 0:
			return io.ErrShortWrite
		case err == nil:
		case !errors.Is(err, os.ErrDeadlineExceeded):
			return err
		case written == 0:
			return errClientStalled
		}
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
