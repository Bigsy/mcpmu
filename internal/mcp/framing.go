package mcp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"sync"
)

// DebugLogging enables verbose payload logging (MCP Send/Recv messages).
var DebugLogging bool

// StdioTransport implements Transport over stdin/stdout pipes.
// Uses NDJSON (newline-delimited JSON) which is the standard for MCP stdio.
//
// Exactly one goroutine ever touches the bufio.Reader: readLines, started by
// NewStdioTransport, which publishes frames on lines. Receive only selects
// between that channel, its caller's context and done, so cancelling a Receive
// cannot leave a reader behind for a later call to race. bufio.Reader is not
// safe for concurrent use, and two readers interleaved in it drop or splice
// frames rather than failing cleanly.
type StdioTransport struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	reader *bufio.Reader

	// lines carries one frame per receive, then a final entry holding the read
	// error. Unbuffered, so a slow consumer applies backpressure to the pipe
	// instead of accumulating frames here.
	lines chan readResult

	// done is closed by Close and unblocks both readLines and any waiting
	// Receive.
	done chan struct{}

	mu     sync.Mutex
	closed bool
}

// NewStdioTransport creates a new stdio transport.
func NewStdioTransport(stdin io.WriteCloser, stdout io.ReadCloser) *StdioTransport {
	t := &StdioTransport{
		stdin:  stdin,
		stdout: stdout,
		reader: bufio.NewReader(stdout),
		lines:  make(chan readResult),
		done:   make(chan struct{}),
	}
	go t.readLines()
	return t
}

// Send writes a message using NDJSON framing (newline-delimited).
func (t *StdioTransport) Send(ctx context.Context, msg []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return fmt.Errorf("transport closed")
	}

	if DebugLogging {
		log.Printf("MCP Send: %s", string(msg))
	}

	// NDJSON: just append newline
	if _, err := t.stdin.Write(msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if _, err := t.stdin.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write newline: %w", err)
	}
	return nil
}

// readResult holds the result of an async read operation.
type readResult struct {
	line []byte
	err  error
}

// readLines is the transport's only reader. It runs until the pipe errors or
// Close is called.
//
// A frame is published before the error that accompanied it: bufio.ReadBytes
// returns the bytes it managed to read alongside io.EOF, so a server that exits
// after writing its last message without a trailing newline would otherwise
// have that message silently discarded. internal/server/server.go's stdin loop
// makes the same allowance.
func (t *StdioTransport) readLines() {
	defer close(t.lines)

	for {
		line, err := t.reader.ReadBytes('\n')

		// ReadBytes' buffer is only valid until the next read, so clone before
		// handing it to another goroutine. Blank lines carry no frame; dropping
		// them here saves the client a malformed-frame rejection.
		if msg := bytes.TrimSpace(line); len(msg) > 0 {
			if !t.publish(readResult{line: append([]byte(nil), msg...)}) {
				return
			}
		}

		if err != nil {
			t.publish(readResult{err: err})
			return
		}
	}
}

// publish hands one result to Receive, reporting false if the transport closed
// first so readLines can exit instead of blocking on a send nobody will take.
func (t *StdioTransport) publish(result readResult) bool {
	select {
	case t.lines <- result:
		return true
	case <-t.done:
		return false
	}
}

// Receive reads the next NDJSON message. A cancelled context abandons this call
// only; the transport stays usable and no buffered input is lost.
func (t *StdioTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case result, ok := <-t.lines:
		if !ok {
			return nil, fmt.Errorf("transport closed")
		}
		if result.err != nil {
			return nil, fmt.Errorf("read line: %w", result.err)
		}
		if DebugLogging {
			log.Printf("MCP Recv: %s", string(result.line))
		}
		return result.line, nil

	case <-ctx.Done():
		return nil, ctx.Err()

	case <-t.done:
		return nil, fmt.Errorf("transport closed")
	}
}

// Close closes the transport. Closing the pipes unblocks readLines if it is in
// a read; closing done unblocks it if it is instead waiting to publish.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true
	close(t.done)

	var errs []error
	if err := t.stdin.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close stdin: %w", err))
	}
	if err := t.stdout.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close stdout: %w", err))
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
