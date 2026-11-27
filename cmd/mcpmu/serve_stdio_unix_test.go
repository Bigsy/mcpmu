//go:build unix

package main

import (
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestEmbeddedOutputInterruptsInheritedPipe(t *testing.T) {
	var fds [2]int
	if err := unix.Pipe(fds[:]); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	original := os.NewFile(uintptr(fds[1]), "inherited-stdout")
	defer original.Close()
	if err := original.SetWriteDeadline(time.Time{}); !errors.Is(err, os.ErrNoDeadline) {
		t.Fatalf("expected a blocking inherited pipe, got deadline error %v", err)
	}
	stream, cleanup, err := interruptibleStdout(original)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Keep the read end open and idle, the same as a slow stdio client.
	if err := stream.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 4096)
	for {
		_, err := stream.Write(chunk)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := stream.Write(chunk)
		done <- err
	}()
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("write returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing embedded output did not interrupt the writer")
	}
}
