package daemon

import (
	"context"
	"errors"
	"io"
	"net"
)

var errOutboundQueueFull = errors.New("daemon session outbound queue is full")

type queuedWriter struct {
	conn   net.Conn
	queue  chan queuedWrite
	done   <-chan struct{}
	cancel context.CancelFunc
	exited chan struct{}
}

type queuedWrite struct {
	data    []byte
	flushed chan struct{}
}

func newQueuedWriter(conn net.Conn, size int, done <-chan struct{}, cancel context.CancelFunc) *queuedWriter {
	if size <= 0 {
		size = 64
	}
	writer := &queuedWriter{
		conn: conn, queue: make(chan queuedWrite, size), done: done,
		cancel: cancel, exited: make(chan struct{}),
	}
	go writer.run()
	return writer
}

func (writer *queuedWriter) Write(data []byte) (int, error) {
	copyOfData := append([]byte(nil), data...)
	select {
	case <-writer.done:
		return 0, io.ErrClosedPipe
	default:
	}
	select {
	case writer.queue <- queuedWrite{data: copyOfData}:
		return len(data), nil
	case <-writer.done:
		return 0, io.ErrClosedPipe
	default:
		writer.cancel()
		_ = writer.conn.Close()
		return 0, errOutboundQueueFull
	}
}

// Flush waits until every write queued before the marker has reached the
// socket. It is used when a half-closed shim input lets a Session finish while
// its final responses are still buffered in the daemon writer.
func (writer *queuedWriter) Flush(ctx context.Context) error {
	flushed := make(chan struct{})
	select {
	case writer.queue <- queuedWrite{flushed: flushed}:
	case <-writer.exited:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-flushed:
		return nil
	case <-writer.exited:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (writer *queuedWriter) run() {
	defer close(writer.exited)
	for {
		select {
		case item := <-writer.queue:
			if item.flushed != nil {
				close(item.flushed)
				continue
			}
			if err := writeAll(writer.conn, item.data); err != nil {
				writer.cancel()
				_ = writer.conn.Close()
				return
			}
		case <-writer.done:
			return
		}
	}
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
