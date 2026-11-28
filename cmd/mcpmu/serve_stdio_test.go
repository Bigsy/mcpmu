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
	for _, shutdown := range []string{"EOF", "cancel"} {
		t.Run(shutdown, func(t *testing.T) {
			input, client := io.Pipe()
			defer func() { _ = client.Close() }()
			output, writer := io.Pipe()
			defer func() { _ = output.Close() }()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			// Model an inherited stdout that cannot interrupt Write.
			opts := server.Options{Config: &config.Config{}, Stdout: struct{ io.Writer }{writer}}
			done := make(chan error, 1)
			go func() { done <- serveEmbeddedSession(ctx, input, opts, 100*time.Millisecond) }()
			if _, err := fmt.Fprintln(client, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); err != nil {
				t.Fatal(err)
			}
			// Receiving only the first byte of a synchronous pipe write
			// proves that the response is already being written.
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
			if shutdown == "EOF" {
				_ = client.Close()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "unable to drain embedded stdio session") {
					t.Fatalf("shutdown returned %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown waited for stdout")
			}
		})
	}
}
