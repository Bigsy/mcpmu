//go:build !windows

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/process"
)

type runningTestDaemon struct {
	configPath string
	paths      Paths
	build      string
	cancel     context.CancelFunc
	done       <-chan error
}

func startTestDaemon(t *testing.T, mutate func(*Options)) runningTestDaemon {
	t.Helper()
	runtimeRoot := makeShortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":1,"servers":{},"namespaces":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalConfigPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := RuntimePaths(canonical)
	if err != nil {
		t.Fatal(err)
	}
	_, build, err := ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		ConfigPath: canonical, Build: build, Version: "test", Revision: "test-revision",
		Linger: 5 * time.Second, DrainTimeout: 500 * time.Millisecond,
		HandshakeTimeout: time.Second, OutboundQueue: 8,
	}
	if mutate != nil {
		mutate(&opts)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, opts)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, probeErr := Control(probeCtx, canonical, "status")
		probeCancel()
		if probeErr == nil {
			break
		}
		select {
		case runErr := <-done:
			cancel()
			t.Fatalf("daemon exited before listening: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("timed out waiting for daemon control: %v", probeErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("daemon did not stop during cleanup")
		}
	})
	return runningTestDaemon{
		configPath: canonical, paths: paths, build: build,
		cancel: cancel, done: done,
	}
}

func dialHandshake(t *testing.T, daemon runningTestDaemon, handshake Handshake) (*net.UnixConn, *bufio.Reader, HandshakeResponse) {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: daemon.paths.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if handshake.ConfigPath == "" {
		handshake.ConfigPath = daemon.configPath
	}
	payload, err := marshalLine(HandshakeEnvelope{Handshake: handshake})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAll(conn, payload); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response HandshakeResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	return conn, reader, response
}

func TestDaemonSessionHandshakeAndMCP(t *testing.T) {
	d := startTestDaemon(t, nil)
	if info, err := os.Stat(d.paths.Socket); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode = %o, want 600", info.Mode().Perm())
	}
	if info, err := os.Stat(d.paths.PIDFile); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("pidfile mode = %o, want 600", info.Mode().Perm())
	}
	conn, reader, response := dialHandshake(t, d, Handshake{
		Type: "session", Protocol: SessionProtocol, Build: d.build,
		Prompts: true,
	})
	defer func() { _ = conn.Close() }()
	if !response.OK {
		t.Fatalf("handshake rejected: %+v", response)
	}

	status, fallback, err := Inspect(context.Background(), d.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if fallback || status.Sessions != 1 || status.Build != d.build || status.ConfigPath != d.configPath {
		t.Fatalf("unexpected status: fallback=%t status=%+v", fallback, status)
	}

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1"}}}` + "\n"
	if _, err := conn.Write([]byte(initialize)); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	if err := json.Unmarshal(line, &message); err != nil {
		t.Fatal(err)
	}
	result, ok := message["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize response has no result: %s", line)
	}
	capabilities := result["capabilities"].(map[string]any)
	if _, ok := capabilities["resources"]; ok {
		t.Fatalf("resources capability present despite session flag: %s", line)
	}
	if _, ok := capabilities["prompts"]; !ok {
		t.Fatalf("prompts capability missing despite session flag: %s", line)
	}
}

func TestSessionHandshakeWriteFailureClosesConnection(t *testing.T) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(makeShortTempDir(t), "ack.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		conn, _ := listener.AcceptUnix()
		accepted <- conn
	}()
	client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	serverConn := <-accepted
	if serverConn == nil {
		t.Fatal("listener did not accept connection")
	}
	_ = client.Close()

	lifetime := t.Context()
	d := &Daemon{
		opts:     Options{Build: "test-build", Linger: time.Hour},
		lifetime: lifetime, sessions: make(map[uint64]*liveSession),
		stopCh: make(chan struct{}),
	}
	d.handleSession(serverConn, bufio.NewReader(serverConn), Handshake{
		Protocol: SessionProtocol,
		Build:    "test-build",
	})
	d.mu.Lock()
	if d.idle != nil {
		d.idle.Stop()
	}
	sessions := len(d.sessions)
	d.mu.Unlock()
	if sessions != 0 {
		t.Fatalf("failed handshake retained %d sessions", sessions)
	}
	if err := serverConn.SetDeadline(time.Now()); err == nil {
		t.Fatal("failed handshake left server connection open")
	}
}

func TestDaemonRejectsMalformedHandshake(t *testing.T) {
	d := startTestDaemon(t, nil)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: d.paths.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("{malformed}\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response HandshakeResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error != "malformed handshake" {
		t.Fatalf("unexpected malformed-handshake response: %+v", response)
	}
}

// TestDaemonRejectsBadCompressionLevel pins that a handshake carrying an
// unknown compression level is refused with a handshake error response rather
// than being silently accepted or dropped. The level now decodes as part of
// the handshake JSON, so the rejection comes from the decode step.
func TestDaemonRejectsBadCompressionLevel(t *testing.T) {
	d := startTestDaemon(t, nil)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: d.paths.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	line := fmt.Sprintf(`{"mcpmu_handshake":{"type":"session","protocol":%d,"build":%q,"configPath":%q,"compression":"bogus"}}`+"\n",
		SessionProtocol, d.build, d.configPath)
	if _, err := conn.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	responseLine, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response HandshakeResponse
	if err := json.Unmarshal(responseLine, &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || !strings.Contains(response.Error, "malformed handshake") {
		t.Fatalf("bad compression level should be rejected, got: %+v", response)
	}
}

func TestDaemonRejectsSessionIdentityMismatches(t *testing.T) {
	d := startTestDaemon(t, nil)
	tests := []struct {
		name      string
		handshake Handshake
		contains  string
	}{
		{name: "protocol", handshake: Handshake{Type: "session", Protocol: SessionProtocol + 1, Build: d.build}, contains: "protocol mismatch"},
		{name: "build", handshake: Handshake{Type: "session", Protocol: SessionProtocol, Build: "different"}, contains: "build mismatch"},
		{name: "config", handshake: Handshake{Type: "session", Protocol: SessionProtocol, Build: d.build, ConfigPath: d.configPath + ".other"}, contains: "config path mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn, _, response := dialHandshake(t, d, test.handshake)
			_ = conn.Close()
			if response.OK || !strings.Contains(response.Error, test.contains) {
				t.Fatalf("response = %+v, want error containing %q", response, test.contains)
			}
			if response.DaemonBuild != d.build || response.DaemonConfigPath != d.configPath {
				t.Fatalf("rejection omitted daemon identity: %+v", response)
			}
		})
	}
}

func TestDaemonControlStatusUnknownAndStop(t *testing.T) {
	d := startTestDaemon(t, nil)
	conn, reader, accepted := dialHandshake(t, d, Handshake{
		Type: "control", ControlProtocol: ControlProtocol,
		Build: "deliberately-different-build",
	})
	if !accepted.OK {
		t.Fatalf("control connection rejected build mismatch: %+v", accepted)
	}
	request, _ := marshalLine(ControlRequest{Command: "status"})
	if err := writeAll(conn, request); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	response, err := Control(context.Background(), d.configPath, "status")
	if err != nil {
		t.Fatal(err)
	}
	if response.Status == nil || response.Status.PID != os.Getpid() || response.Status.Version != "test" {
		t.Fatalf("unexpected status response: %+v", response)
	}
	if _, err := Control(context.Background(), d.configPath, "not-a-command"); err == nil || !strings.Contains(err.Error(), "unknown control command") {
		t.Fatalf("unknown command error = %v", err)
	}

	fallback, err := Stop(context.Background(), d.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if fallback {
		t.Fatal("live control unexpectedly used pidfile fallback")
	}
	select {
	case err := <-d.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

func TestDaemonStopDrainTimeoutDisconnectsSession(t *testing.T) {
	d := startTestDaemon(t, func(opts *Options) { opts.DrainTimeout = 50 * time.Millisecond })
	conn, _, response := dialHandshake(t, d, Handshake{
		Type: "session", Protocol: SessionProtocol, Build: d.build,
	})
	if !response.OK {
		t.Fatalf("handshake rejected: %+v", response)
	}
	if _, err := Control(context.Background(), d.configPath, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if _, err := conn.Read(buffer); err == nil {
		t.Fatal("session remained connected after drain timeout")
	}
	select {
	case err := <-d.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit after drain timeout")
	}
}

func TestDaemonLingerCancelledByReconnect(t *testing.T) {
	d := startTestDaemon(t, func(opts *Options) { opts.Linger = 120 * time.Millisecond })
	conn, _, response := dialHandshake(t, d, Handshake{
		Type: "session", Protocol: SessionProtocol, Build: d.build,
	})
	if !response.OK {
		t.Fatalf("handshake rejected: %+v", response)
	}
	_ = conn.Close()
	time.Sleep(60 * time.Millisecond)
	second, _, response := dialHandshake(t, d, Handshake{
		Type: "session", Protocol: SessionProtocol, Build: d.build,
	})
	if !response.OK {
		t.Fatalf("reconnect rejected: %+v", response)
	}
	time.Sleep(90 * time.Millisecond)
	select {
	case err := <-d.done:
		t.Fatalf("daemon exited while reconnected: %v", err)
	default:
	}
	_ = second.Close()
	select {
	case err := <-d.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not exit after final linger")
	}
}

func TestRunLockRejectsSecondDaemon(t *testing.T) {
	d := startTestDaemon(t, nil)
	err := Run(context.Background(), Options{ConfigPath: d.configPath, Build: d.build})
	if err == nil || !strings.Contains(err.Error(), "another daemon already owns") {
		t.Fatalf("second Run() error = %v", err)
	}
}

func TestDaemonRunsWithAbsentConfigAndParent(t *testing.T) {
	runtimeRoot := makeShortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	configPath := filepath.Join(t.TempDir(), "not-created", "nested", "config.json")
	canonical, err := CanonicalConfigPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	_, build, err := ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{ConfigPath: configPath, Build: build, Linger: time.Second})
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("absent-config daemon did not stop")
		}
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		response, controlErr := Control(probeCtx, configPath, "status")
		probeCancel()
		if controlErr == nil {
			if response.Status == nil || response.Status.ConfigPath != canonical {
				t.Fatalf("unexpected absent-config status: %+v", response)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("absent-config daemon did not become ready: %v", controlErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("absent-config daemon did not exit")
	}
}

func TestReadBoundedLineRejectsOversize(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 32)+"\n"), 16)
	if _, err := readBoundedLine(reader, 16); err == nil {
		t.Fatal("oversized line was accepted")
	}
}

func TestQueuedWriterDisconnectsFullQueue(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	writer := newQueuedWriter(serverConn, 1, ctx.Done(), cancel)

	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(writer.queue) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := writer.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("third")); !errors.Is(err, errOutboundQueueFull) {
		t.Fatalf("third write error = %v, want %v", err, errOutboundQueueFull)
	}
	select {
	case <-writer.exited:
	case <-time.After(time.Second):
		t.Fatal("writer did not exit after queue exhaustion")
	}
}

func TestPIDFileValidationUsesFullIdentity(t *testing.T) {
	executable, _, err := ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	record, err := newPIDFile("/canonical/config.json", executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePIDFile(record, record.ConfigPath); err != nil {
		t.Fatalf("valid current-process pidfile rejected: %v", err)
	}
	if err := ValidatePIDFile(record, "/hash-collision/config.json"); err == nil || !strings.Contains(err.Error(), "config path mismatch") {
		t.Fatalf("wrong full config validation error = %v", err)
	}
	reused := record
	reused.ProcessStartIdentity++
	if err := ValidatePIDFile(reused, reused.ConfigPath); err == nil || !strings.Contains(err.Error(), "was reused") {
		t.Fatalf("wrong start identity validation error = %v", err)
	}
	wrongExecutable := record
	wrongExecutable.ExecutablePath = filepath.Join(t.TempDir(), "different")
	if err := ValidatePIDFile(wrongExecutable, wrongExecutable.ConfigPath); err == nil {
		t.Fatal("wrong executable path was accepted")
	}
}

func TestInspectRejectsCollidingPIDFile(t *testing.T) {
	runtimeRoot := makeShortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	requested := filepath.Join(t.TempDir(), "config.json")
	canonical, err := CanonicalConfigPath(requested)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := RuntimePaths(canonical)
	if err != nil {
		t.Fatal(err)
	}
	executable, _, err := ExecutableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	record, err := newPIDFile(canonical+".collision", executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePIDFile(paths.PIDFile, record); err != nil {
		t.Fatal(err)
	}
	_, _, err = Inspect(context.Background(), canonical)
	if err == nil || !strings.Contains(err.Error(), "config path mismatch") {
		t.Fatalf("Inspect() error = %v", err)
	}
}

func TestStopFallsBackToValidatedPIDFile(t *testing.T) {
	runtimeRoot := makeShortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	configPath, err := CanonicalConfigPath(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	paths, err := RuntimePaths(configPath)
	if err != nil {
		t.Fatal(err)
	}

	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	t.Cleanup(func() {
		if finished || child.Process == nil {
			return
		}
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	startIdentity, err := process.ProcessStartIdentity(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := processExecutablePath(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	record := PIDFile{
		ConfigPath: configPath, PID: child.Process.Pid, StartedAt: time.Now().UTC(),
		ProcessStartIdentity: startIdentity, ExecutablePath: executable,
	}
	if err := writePIDFile(paths.PIDFile, record); err != nil {
		t.Fatal(err)
	}
	fallback, err := Stop(context.Background(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !fallback {
		t.Fatal("Stop() did not report pidfile fallback")
	}
	waitErr := child.Wait()
	finished = true
	if waitErr == nil {
		t.Fatal("sleep exited successfully after SIGTERM; want signal exit")
	}
}
