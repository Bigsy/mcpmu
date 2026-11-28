package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/server"
)

func TestEmbeddedShutdownWithBlockedOutput(t *testing.T) {
	t.Run("EOF", func(t *testing.T) {
		client, done := startEmbeddedWithBlockedOutput(t, t.Context())
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		requireEmbeddedDrainTimeout(t, done)
	})

	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		_, done := startEmbeddedWithBlockedOutput(t, ctx)
		cancel()
		requireEmbeddedDrainTimeout(t, done)
	})
}

func startEmbeddedWithBlockedOutput(t *testing.T, ctx context.Context) (*io.PipeWriter, <-chan error) {
	t.Helper()
	input, client := io.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	output, writer := io.Pipe()
	t.Cleanup(func() { _ = output.Close() })

	// Model an inherited stdout that cannot interrupt Write.
	opts := server.Options{Config: &config.Config{}, Stdout: struct{ io.Writer }{writer}}
	done := make(chan error, 1)
	go func() { done <- serveEmbeddedSession(ctx, input, opts, 100*time.Millisecond) }()
	if _, err := fmt.Fprintln(client, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); err != nil {
		t.Fatal(err)
	}

	// Reading only the first byte leaves the response blocked inside Write.
	first := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := io.ReadFull(output, b[:])
		first <- err
	}()
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not begin writing the response")
	}
	return client, done
}

func requireEmbeddedDrainTimeout(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unable to drain embedded stdio session") {
			t.Fatalf("shutdown returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited for stdout")
	}
}
