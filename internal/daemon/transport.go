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
	queue  chan []byte
	done   <-chan struct{}
	cancel context.CancelFunc
	exited chan struct{}
}

func newQueuedWriter(conn net.Conn, size int, done <-chan struct{}, cancel context.CancelFunc) *queuedWriter {
	if size <= 0 {
		size = 64
	}
	writer := &queuedWriter{
		conn: conn, queue: make(chan []byte, size), done: done,
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
	case writer.queue <- copyOfData:
		return len(data), nil
	case <-writer.done:
		return 0, io.ErrClosedPipe
	default:
		writer.cancel()
		_ = writer.conn.Close()
		return 0, errOutboundQueueFull
	}
}

func (writer *queuedWriter) run() {
	defer close(writer.exited)
	for {
		select {
		case data := <-writer.queue:
			if err := writeAll(writer.conn, data); err != nil {
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
