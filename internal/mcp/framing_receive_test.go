package mcp

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// blockingReader is a pipe whose Read blocks until data is pushed and whose
// Close does not interrupt a Read already in flight. Plenty of real readers
// behave this way, and it makes the "second Receive spawns a second reader"
// window deterministic instead of timing-dependent.
type blockingReader struct {
	chunks chan []byte

	mu     sync.Mutex
	closed bool
	rest   []byte
}

func newBlockingReader() *blockingReader {
	return &blockingReader{chunks: make(chan []byte, 8)}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	if len(r.rest) > 0 {
		n := copy(p, r.rest)
		r.rest = r.rest[n:]
		r.mu.Unlock()
		return n, nil
	}
	r.mu.Unlock()

	chunk, ok := <-r.chunks
	if !ok {
		return 0, io.EOF
	}
	r.mu.Lock()
	n := copy(p, chunk)
	if n < len(chunk) {
		r.rest = append(r.rest, chunk[n:]...)
	}
	r.mu.Unlock()
	return n, nil
}

func (r *blockingReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		close(r.chunks)
	}
	return nil
}

func (r *blockingReader) push(b []byte) { r.chunks <- b }

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

// TestReceiveDoesNotSpawnSecondReader guards against a cancelled Receive
// leaving its reader goroutine blocked on the shared bufio.Reader while a
// later Receive starts another one.
//
// Two goroutines inside bufio.Reader at once corrupts its buffer state and can
// drop or splice frames. Before the fix, Receive spawned a goroutine per call
// and abandoned it on ctx.Done(), so this test failed under -race with a DATA
// RACE inside bufio.(*Reader).ReadBytes. It is latent in production only
// because readLoop passes context.Background(); one cancellable context turns
// it into frame corruption.
func TestReceiveDoesNotSpawnSecondReader(t *testing.T) {
	reader := newBlockingReader()
	transport := NewStdioTransport(discardWriteCloser{}, reader)
	defer func() { _ = transport.Close() }()

	// First Receive blocks in the underlying Read, then is abandoned by its
	// caller's context.
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = transport.Receive(ctx)
	}()

	// Let the first read reach the blocking Read before cancelling.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-firstDone

	// Second Receive: with a per-call goroutine, this one races the abandoned
	// first reader as soon as data arrives.
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer secondCancel()

	received := make(chan []byte, 1)
	go func() {
		msg, err := transport.Receive(secondCtx)
		if err == nil {
			received <- msg
		} else {
			received <- nil
		}
	}()

	time.Sleep(50 * time.Millisecond)
	reader.push([]byte("{\"jsonrpc\":\"2.0\",\"id\":1}\n"))
	reader.push([]byte("{\"jsonrpc\":\"2.0\",\"id\":2}\n"))

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("second Receive did not return")
	}
}

// TestReceiveDeliversTrailingLineWithoutNewline guards the final message of a
// server that exits without writing a trailing newline.
//
// bufio.Reader.ReadBytes returns the bytes it read alongside io.EOF. Before the
// fix, Receive checked err first and dropped those bytes, silently losing the
// message. internal/server/server.go's own stdin loop gets this right (it
// processes the line before handling the error); the two are now consistent.
func TestReceiveDeliversTrailingLineWithoutNewline(t *testing.T) {
	reader := newBlockingReader()
	transport := NewStdioTransport(discardWriteCloser{}, reader)
	defer func() { _ = transport.Close() }()

	const payload = `{"jsonrpc":"2.0","id":7,"result":{}}`
	reader.push([]byte(payload)) // no trailing newline
	_ = reader.Close()           // EOF follows immediately

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg, err := transport.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive dropped the trailing message: err = %v, want the payload", err)
	}
	if string(msg) != payload {
		t.Fatalf("Receive = %q, want %q", msg, payload)
	}

	// The EOF must still be reported on the following call, not swallowed.
	if _, err := transport.Receive(ctx); err == nil {
		t.Fatal("second Receive after EOF returned nil error, want an error")
	} else if !errors.Is(err, io.EOF) {
		t.Logf("second Receive error = %v (not io.EOF, acceptable if wrapped)", err)
	}
}
