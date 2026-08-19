package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// newServeFlagSet builds a throwaway command carrying the serve flag set, so
// validateHTTPServeFlags can be exercised without touching the package-level
// serveCmd (whose flag values persist across tests).
func newServeFlagSet(t *testing.T, args []string) *cobra.Command {
	t.Helper()
	t.Cleanup(func() {
		serveHTTP = false
		serveIsolated = false
		serveToken = ""
		serveAddr = ""
		serveAllowOrigins = nil
		serveSessionIdleTimeout = 0
	})
	cmd := &cobra.Command{Use: "serve"}
	cmd.Flags().BoolVar(&serveHTTP, "http", false, "")
	cmd.Flags().BoolVar(&serveIsolated, "isolated", false, "")
	cmd.Flags().StringVar(&serveAddr, "addr", "127.0.0.1:8081", "")
	cmd.Flags().StringVar(&serveToken, "token", "", "")
	cmd.Flags().StringArrayVar(&serveAllowOrigins, "allow-origin", nil, "")
	cmd.Flags().DurationVar(&serveSessionIdleTimeout, "session-idle-timeout", 0, "")
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

func TestValidateHTTPServeFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"plain http is fine", []string{"--http"}, ""},
		{"isolated rejected with http", []string{"--http", "--isolated"}, "--isolated"},
		{"addr requires http", []string{"--addr", ":9999"}, "--addr requires --http"},
		{"token requires http", []string{"--token", "t"}, "--token requires --http"},
		{"allow-origin requires http", []string{"--allow-origin", "https://x"}, "--allow-origin requires --http"},
		{"idle timeout requires http", []string{"--session-idle-timeout", "1h"}, "--session-idle-timeout requires --http"},
		{"stdio serve untouched", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newServeFlagSet(t, tc.args)
			err := validateHTTPServeFlags(cmd)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestHTTPServeTokenFromEnv(t *testing.T) {
	t.Setenv("MCPMU_SERVE_TOKEN", "from-env")
	cmd := newServeFlagSet(t, []string{"--http"})
	if err := validateHTTPServeFlags(cmd); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if serveToken != "from-env" {
		t.Fatalf("serveToken = %q, want from-env", serveToken)
	}

	cmd = newServeFlagSet(t, []string{"--http", "--token", "from-flag"})
	if err := validateHTTPServeFlags(cmd); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if serveToken != "from-flag" {
		t.Fatalf("serveToken = %q, want flag to win over env", serveToken)
	}
}

// fakeListener stands in for *httpserve.Server: ListenAndServe returns the
// instant Shutdown starts (what net/http actually does — Shutdown closes
// listeners first and drains afterwards), and Shutdown itself takes a while.
type fakeListener struct {
	drain        time.Duration
	closed       chan struct{}
	shutdownDone atomic.Bool
	serveErr     error
}

func (f *fakeListener) ListenAndServe() error {
	if f.serveErr != nil {
		return f.serveErr
	}
	<-f.closed
	return nil
}

func (f *fakeListener) Shutdown(context.Context) error {
	close(f.closed) // unblocks ListenAndServe, as closing the listener does
	time.Sleep(f.drain)
	f.shutdownDone.Store(true)
	return nil
}

// TestServeUntilShutdownWaitsForDrain pins the ordering the caller depends on:
// runHTTPServe's deferred core.Close() stops upstream processes and writes the
// final metrics flush, so it must not run while requests are still draining.
func TestServeUntilShutdownWaitsForDrain(t *testing.T) {
	srv := &fakeListener{drain: 150 * time.Millisecond, closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // shut down immediately

	if err := serveUntilShutdown(ctx, srv, time.Second); err != nil {
		t.Fatalf("serveUntilShutdown: %v", err)
	}
	if !srv.shutdownDone.Load() {
		t.Fatal("returned while Shutdown was still draining")
	}
}

// A listener that never came up has nothing to drain: the error is returned
// rather than parked behind a Shutdown that will not be called.
func TestServeUntilShutdownReturnsListenerError(t *testing.T) {
	srv := &fakeListener{closed: make(chan struct{}), serveErr: errors.New("address in use")}
	err := serveUntilShutdown(context.Background(), srv, time.Second)
	if err == nil || !strings.Contains(err.Error(), "address in use") {
		t.Fatalf("error = %v, want it to carry the listener failure", err)
	}
}
