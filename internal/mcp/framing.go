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
type StdioTransport struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	reader *bufio.Reader
	mu     sync.Mutex
	closed bool
}

// NewStdioTransport creates a new stdio transport.
func NewStdioTransport(stdin io.WriteCloser, stdout io.ReadCloser) *StdioTransport {
	return &StdioTransport{
		stdin:  stdin,
		stdout: stdout,
		reader: bufio.NewReader(stdout),
	}
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

// Receive reads the next NDJSON message.
// Respects context cancellation by closing the underlying pipe when cancelled.
func (t *StdioTransport) Receive(ctx context.Context) ([]byte, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, fmt.Errorf("transport closed")
	}
	t.mu.Unlock()

	// Run the blocking read in a goroutine
	resultCh := make(chan readResult, 1)
	go func() {
		line, err := t.reader.ReadBytes('\n')
		resultCh <- readResult{line: line, err: err}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			return nil, fmt.Errorf("read line: %w", result.err)
		}
		msg := bytes.TrimSpace(result.line)
		if DebugLogging {
			log.Printf("MCP Recv: %s", string(msg))
		}
		return msg, nil

	case <-ctx.Done():
		// Close stdout to unblock the read goroutine
		// The goroutine will get an error and exit
		_ = t.stdout.Close()
		return nil, ctx.Err()
	}
}

// Close closes the transport.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true

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
