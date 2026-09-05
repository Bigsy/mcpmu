//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/daemon"
)

func TestStatusControlFixture(t *testing.T) {
	for _, mode := range []string{"running", "incompatible", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "ms-")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(dir) }()
			t.Setenv("XDG_RUNTIME_DIR", dir)
			old := configPath
			configPath = filepath.Join(dir, "config.json")
			defer func() { configPath = old }()
			canonical, err := daemon.CanonicalConfigPath(configPath)
			if err != nil {
				t.Fatal(err)
			}
			paths, err := daemon.RuntimePaths(canonical)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", paths.Socket)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			_, build, err := daemon.ExecutableIdentity()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "incompatible" {
				build = "other-build"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, e := listener.Accept()
				if e != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				if mode == "timeout" {
					<-ctx.Done()
					return
				}
				decoder := json.NewDecoder(conn)
				encoder := json.NewEncoder(conn)
				var request map[string]any
				if decoder.Decode(&request) != nil {
					return
				}
				if encoder.Encode(daemon.HandshakeResponse{OK: true}) != nil {
					return
				}
				if decoder.Decode(&request) != nil {
					return
				}
				_ = encoder.Encode(daemon.ControlResponse{OK: true, Status: &daemon.StatusResponse{ConfigPath: canonical, Build: build, RunningUpstreams: []string{"fixture"}, Sessions: 2}})
			}()
			report, err := collectDiagnostics(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			want := mode
			if mode == "timeout" {
				want = "unavailable"
			}
			if report.DaemonState != want {
				t.Fatalf("got %+v", report)
			}
			if mode != "timeout" && (report.Daemon == nil || report.Daemon.Sessions != 2) {
				t.Fatalf("status missing: %+v", report)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("fixture did not finish")
			}
		})
	}
}
