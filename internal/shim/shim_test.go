//go:build !windows

package shim

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/daemon"
)

func shortRuntimeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mu-shim-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func writeShimConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"servers":{},"namespaces":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testDaemonOptions(t *testing.T, configPath string) daemon.Options {
	t.Helper()
	_, build, err := daemon.ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return daemon.Options{
		ConfigPath: configPath, Build: build, Version: "test",
		Linger: 5 * time.Second, DrainTimeout: time.Second,
		HandshakeTimeout: time.Second,
	}
}

func waitForDaemon(t *testing.T, configPath string, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := daemon.Control(ctx, configPath, "status")
		cancel()
		if err == nil {
			return
		}
		select {
		case runErr := <-done:
			t.Fatalf("daemon stopped before ready: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConnectOrSpawnJoinsExistingCanonicalDaemon(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	configPath := writeShimConfig(t)
	linkPath := filepath.Join(t.TempDir(), "linked.json")
	if err := os.Symlink(configPath, linkPath); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, testDaemonOptions(t, configPath)) }()
	waitForDaemon(t, configPath, done)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("daemon did not stop")
		}
	})

	var spawnCalls atomic.Int32
	connection, err := ConnectOrSpawn(context.Background(), Options{
		ConfigPath: linkPath, Resources: true, Prompts: true,
		spawn: func(string, []string, string) error {
			spawnCalls.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Conn.Close() }()
	if spawnCalls.Load() != 0 {
		t.Fatalf("spawn called %d times for existing daemon", spawnCalls.Load())
	}
}

func TestConnectOrSpawnConcurrentColdStartCallsSpawnerOnce(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	configPath := writeShimConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var spawnCalls atomic.Int32
	spawn := func(_ string, _ []string, _ string) error {
		if spawnCalls.Add(1) == 1 {
			go func() { done <- daemon.Run(ctx, testDaemonOptions(t, configPath)) }()
		}
		return nil
	}

	const clients = 8
	connections := make(chan *Connection, clients)
	errorsCh := make(chan error, clients)
	for range clients {
		go func() {
			connection, err := ConnectOrSpawn(context.Background(), Options{
				ConfigPath: configPath, Resources: true, Prompts: true,
				startupTimeout: 3 * time.Second, spawn: spawn,
			})
			if err != nil {
				errorsCh <- err
				return
			}
			connections <- connection
		}()
	}
	var opened []*Connection
	for range clients {
		select {
		case err := <-errorsCh:
			cancel()
			t.Fatal(err)
		case connection := <-connections:
			opened = append(opened, connection)
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("timed out waiting for concurrent shims")
		}
	}
	if spawnCalls.Load() != 1 {
		t.Fatalf("spawn calls = %d, want 1", spawnCalls.Load())
	}
	for _, connection := range opened {
		_ = connection.Conn.Close()
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

func TestConnectOrSpawnDoesNotReplaceRejectedDaemon(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	configPath := writeShimConfig(t)
	canonical, err := daemon.CanonicalConfigPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := daemon.RuntimePaths(canonical)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: paths.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		conn, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		response, _ := json.Marshal(daemon.HandshakeResponse{
			Error: "daemon build mismatch", DaemonBuild: "other", DaemonConfigPath: canonical,
		})
		_, _ = conn.Write(append(response, '\n'))
	}()
	var spawnCalls atomic.Int32
	_, err = ConnectOrSpawn(context.Background(), Options{
		ConfigPath: configPath,
		spawn:      func(string, []string, string) error { spawnCalls.Add(1); return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "build mismatch") {
		t.Fatalf("ConnectOrSpawn error = %v", err)
	}
	if spawnCalls.Load() != 0 {
		t.Fatalf("rejected live daemon triggered %d spawns", spawnCalls.Load())
	}
}

func TestConnectOrSpawnCanonicalConfigPropagation(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	existing := writeShimConfig(t)
	symlink := filepath.Join(t.TempDir(), "linked.json")
	if err := os.Symlink(existing, symlink); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(t.TempDir(), "missing", "config.json")
	wantSpawnError := errors.New("deliberate spawn failure")
	for _, path := range []string{existing, symlink, absent} {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			canonical, err := daemon.CanonicalConfigPath(path)
			if err != nil {
				t.Fatal(err)
			}
			var gotArgs []string
			_, err = ConnectOrSpawn(context.Background(), Options{
				ConfigPath: path,
				spawn: func(_ string, args []string, _ string) error {
					gotArgs = append([]string(nil), args...)
					return wantSpawnError
				},
			})
			if !errors.Is(err, wantSpawnError) {
				t.Fatalf("ConnectOrSpawn error = %v, want spawn failure", err)
			}
			if len(gotArgs) < 2 || gotArgs[0] != "--config" || gotArgs[1] != canonical {
				t.Fatalf("spawn args = %q, want canonical config %q", gotArgs, canonical)
			}
		})
	}
}

func TestConnectOrSpawnRemovesStaleSocket(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	configPath := writeShimConfig(t)
	canonical, err := daemon.CanonicalConfigPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := daemon.RuntimePaths(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Socket, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	wantSpawnError := errors.New("spawn reached")
	_, err = ConnectOrSpawn(context.Background(), Options{
		ConfigPath: configPath,
		spawn:      func(string, []string, string) error { return wantSpawnError },
	})
	if !errors.Is(err, wantSpawnError) {
		t.Fatalf("ConnectOrSpawn error = %v, want spawn hook", err)
	}
	if _, err := os.Stat(paths.Socket); !os.IsNotExist(err) {
		t.Fatalf("stale socket remains: %v", err)
	}
}

func TestPumpHalfClosesInputAndDrainsOutput(t *testing.T) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(shortRuntimeDir(t), "pump.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer func() { _ = conn.Close() }()
		payload, readErr := io.ReadAll(conn)
		if readErr == nil && string(payload) != "request\n" {
			readErr = io.ErrUnexpectedEOF
		}
		if readErr == nil {
			_, readErr = conn.Write([]byte("response\n"))
		}
		serverDone <- readErr
	}()
	conn, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := Pump(context.Background(), &Connection{Conn: conn, Reader: bufio.NewReader(conn)}, strings.NewReader("request\n"), &output); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if output.String() != "response\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestPumpReturnsOnDaemonEOFWithOpenInput(t *testing.T) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(shortRuntimeDir(t), "eof.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		conn, acceptErr := listener.AcceptUnix()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	conn, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	inputReader, inputWriter := io.Pipe()
	defer func() { _ = inputWriter.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- Pump(context.Background(), &Connection{Conn: conn, Reader: bufio.NewReader(conn)}, inputReader, io.Discard)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Pump did not return on daemon EOF")
	}
}
