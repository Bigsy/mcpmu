//go:build !windows

package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func startQueuedWriter(t *testing.T, budget int, stall time.Duration) (*queuedWriter, net.Conn, context.Context) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	writer := newQueuedWriter(serverConn, budget, stall, ctx.Done(), cancel)
	t.Cleanup(func() {
		cancel()
		_ = serverConn.Close()
		_ = clientConn.Close()
		<-writer.exited
	})
	return writer, clientConn, ctx
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestQueuedWriterHoldsBurstUntilClientReads(t *testing.T) {
	writer, client, ctx := startQueuedWriter(t, 1<<20, 5*time.Second)
	const frames = 1000 // far past the old 64-frame limit
	for i := range frames {
		if _, err := fmt.Fprintf(writer, "{\"id\":%d}\n", i); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	reader := bufio.NewReader(client)
	for i := range frames {
		line, err := reader.ReadString('\n')
		if err != nil || line != fmt.Sprintf("{\"id\":%d}\n", i) {
			t.Fatalf("frame %d = %q, %v", i, line, err)
		}
	}
	if ctx.Err() != nil {
		t.Fatal("session was cancelled")
	}
}

func TestQueuedWriterDropsSessionOverBudget(t *testing.T) {
	writer, _, ctx := startQueuedWriter(t, 64, 5*time.Second)
	frame := bytes.Repeat([]byte("x"), 40)
	// Nobody reads, so the first frame stays in flight and counts against the budget.
	if _, err := writer.Write(frame); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(frame); !errors.Is(err, errOutboundBudgetExceeded) {
		t.Fatalf("second write error = %v, want %v", err, errOutboundBudgetExceeded)
	}
	if _, err := writer.Write(frame); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write after disconnect = %v, want %v", err, io.ErrClosedPipe)
	}
	waitClosed(t, ctx.Done(), "session cancellation")
	waitClosed(t, writer.exited, "writer exit")
}

func TestQueuedWriterDeliversOversizedFrameAlone(t *testing.T) {
	writer, client, ctx := startQueuedWriter(t, 16, 5*time.Second)
	frame := append(bytes.Repeat([]byte("x"), 1000), '\n')
	if _, err := writer.Write(frame); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil || !bytes.Equal(line, frame) {
		t.Fatalf("oversized frame: %d bytes, %v", len(line), err)
	}
	if ctx.Err() != nil {
		t.Fatal("session was cancelled")
	}
}

func TestQueuedWriterDropsStalledClient(t *testing.T) {
	writer, client, ctx := startQueuedWriter(t, 1<<20, 50*time.Millisecond)
	if _, err := writer.Write([]byte("never read\n")); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, ctx.Done(), "session cancellation after the stall timeout")
	waitClosed(t, writer.exited, "writer exit")
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("client connection is still open")
	}
}

func TestQueuedWriterKeepsSlowReader(t *testing.T) {
	const stall = 200 * time.Millisecond
	writer, client, ctx := startQueuedWriter(t, 1<<20, stall)
	frame := append(bytes.Repeat([]byte("x"), 64<<10), '\n')
	if _, err := writer.Write(frame); err != nil {
		t.Fatal(err)
	}
	// Each 1 KiB read lands well inside the stall interval, but the whole
	// frame spans several intervals.
	got := make([]byte, 0, len(frame))
	buf := make([]byte, 1024)
	start := time.Now()
	for len(got) < len(frame) {
		time.Sleep(10 * time.Millisecond)
		n, err := client.Read(buf)
		if err != nil {
			t.Fatalf("after %d bytes: %v", len(got), err)
		}
		got = append(got, buf[:n]...)
	}
	if elapsed := time.Since(start); elapsed < 2*stall {
		t.Fatalf("frame took %v; it must span several stall intervals to test anything", elapsed)
	}
	if !bytes.Equal(got, frame) || ctx.Err() != nil {
		t.Fatalf("slow reader: %d bytes, session err %v", len(got), ctx.Err())
	}
}

func TestQueuedWriterFlushWaitsForBacklog(t *testing.T) {
	writer, client, _ := startQueuedWriter(t, 1<<20, 5*time.Second)
	for i := range 3 {
		if _, err := fmt.Fprintf(writer, "frame %d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	flushed := make(chan error, 1)
	go func() { flushed <- writer.Flush(context.Background()) }()
	select {
	case err := <-flushed:
		t.Fatalf("Flush returned %v before the client read anything", err)
	case <-time.After(50 * time.Millisecond):
	}
	reader := bufio.NewReader(client)
	for range 3 {
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-flushed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Flush did not return after the backlog drained")
	}
}
