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

	log.Printf("MCP Send: %s", string(msg))

	// NDJSON: just append newline
	if _, err := t.stdin.Write(msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if _, err := t.stdin.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write newline: %w", err)
	}
	return nil
}

// Receive reads the next NDJSON message.
func (t *StdioTransport) Receive(ctx context.Context) ([]byte, error) {
	if t.closed {
		return nil, fmt.Errorf("transport closed")
	}

	line, err := t.reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read line: %w", err)
	}

	msg := bytes.TrimSpace(line)
	log.Printf("MCP Recv: %s", string(msg))
	return msg, nil
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
