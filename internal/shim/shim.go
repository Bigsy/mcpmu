// Package shim connects a stdio MCP client to the per-config shared daemon.
package shim

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/Bigsy/mcpmu/internal/daemon"
)

const (
	defaultStartupTimeout   = 5 * time.Second
	defaultHandshakeTimeout = 5 * time.Second
	defaultDrainTimeout     = 5 * time.Second
	maxHandshakeLine        = 64 * 1024
)

// Options are the per-serve properties sent to the daemon. Test-only timing
// and spawn hooks are intentionally unexported.
type Options struct {
	ConfigPath         string
	Namespace          string
	LogLevel           string
	ExposeManagerTools bool
	Resources          bool
	Prompts            bool
	Eager              bool
	// Compression is the tri-state --compress flag as a string ("" = flag
	// absent, "off" = explicit off, otherwise the level); the shim forwards it
	// opaquely in the handshake and the daemon parses it.
	Compression string

	startupTimeout   time.Duration
	handshakeTimeout time.Duration
	spawn            func(executable string, args []string, logPath string) error
}

// Connection is an accepted daemon session. Reader retains any bytes buffered
// while consuming the handshake response.
type Connection struct {
	Conn   *net.UnixConn
	Reader *bufio.Reader
}

// ConnectOrSpawn connects to the daemon for the canonical config path. When
// no daemon is reachable it serializes spawn, removes only provably stale
// sockets, starts the current executable detached, and waits for readiness.
func ConnectOrSpawn(ctx context.Context, opts Options) (*Connection, error) {
	canonical, err := daemon.CanonicalConfigPath(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	opts.ConfigPath = canonical
	if opts.startupTimeout <= 0 {
		opts.startupTimeout = defaultStartupTimeout
	}
	if opts.handshakeTimeout <= 0 {
		opts.handshakeTimeout = defaultHandshakeTimeout
	}
	if opts.spawn == nil {
		opts.spawn = spawnDetached
	}
	paths, err := daemon.RuntimePaths(canonical)
	if err != nil {
		return nil, err
	}
	executable, build, err := daemon.ExecutableIdentity()
	if err != nil {
		return nil, err
	}

	operationCtx, cancel := context.WithTimeout(ctx, opts.startupTimeout)
	defer cancel()
	if conn, reached, connectErr := dialSession(operationCtx, paths.Socket, build, opts); connectErr == nil {
		return conn, nil
	} else if reached {
		return nil, connectErr
	}

	spawnLock, err := acquireLock(operationCtx, paths.SpawnLock)
	if err != nil {
		return nil, fmt.Errorf("acquire daemon spawn lock: %w", err)
	}
	defer spawnLock.Close()

	if conn, reached, connectErr := dialSession(operationCtx, paths.Socket, build, opts); connectErr == nil {
		return conn, nil
	} else if reached {
		return nil, connectErr
	}

	runLock, runLockErr := tryAcquireLock(paths.RunLock)
	if runLockErr != nil {
		// A daemon may have acquired its lifetime lock but not started accepting
		// yet (for example, a manually launched daemon). Wait for that owner.
		return waitForSession(operationCtx, paths.Socket, build, opts)
	}
	if err := os.Remove(paths.Socket); err != nil && !os.IsNotExist(err) {
		runLock.Close()
		return nil, fmt.Errorf("remove stale daemon socket: %w", err)
	}
	runLock.Close()

	args := []string{"--config", canonical, "daemon", "run", "--log-level", opts.LogLevel}
	if err := opts.spawn(executable, args, paths.LogFile); err != nil {
		return nil, fmt.Errorf("spawn daemon: %w", err)
	}
	return waitForSession(operationCtx, paths.Socket, build, opts)
}

func waitForSession(ctx context.Context, socket, build string, opts Options) (*Connection, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		conn, reached, err := dialSession(ctx, socket, build, opts)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if reached {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for daemon startup: %w (last connect error: %v)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func dialSession(ctx context.Context, socket, build string, opts Options) (*Connection, bool, error) {
	dialer := net.Dialer{}
	raw, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, false, fmt.Errorf("connect to daemon: %w", err)
	}
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()
		return nil, true, fmt.Errorf("daemon connection is not a unix socket")
	}
	fail := func(err error) (*Connection, bool, error) {
		_ = conn.Close()
		return nil, true, err
	}
	deadline := time.Now().Add(opts.handshakeTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fail(fmt.Errorf("set daemon handshake deadline: %w", err))
	}
	payload, err := json.Marshal(daemon.HandshakeEnvelope{Handshake: daemon.Handshake{
		Type: "session", Protocol: daemon.SessionProtocol, Build: build,
		ConfigPath: opts.ConfigPath, Namespace: opts.Namespace,
		ExposeManagerTools: opts.ExposeManagerTools, Resources: opts.Resources,
		Prompts: opts.Prompts, Eager: opts.Eager, Compression: opts.Compression,
		PID: os.Getpid(),
	}})
	if err != nil {
		return fail(fmt.Errorf("marshal daemon handshake: %w", err))
	}
	payload = append(payload, '\n')
	if err := writeAll(conn, payload); err != nil {
		return fail(fmt.Errorf("write daemon handshake: %w", err))
	}
	reader := bufio.NewReaderSize(conn, maxHandshakeLine)
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxHandshakeLine {
		return fail(fmt.Errorf("daemon handshake response exceeds %d bytes", maxHandshakeLine))
	}
	if err != nil {
		return fail(fmt.Errorf("read daemon handshake: %w", err))
	}
	var response daemon.HandshakeResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return fail(fmt.Errorf("parse daemon handshake: %w", err))
	}
	if !response.OK {
		message := response.Error
		if message == "" {
			message = "daemon rejected session"
		}
		return fail(fmt.Errorf("%s (daemon build=%q config=%q)", message, response.DaemonBuild, response.DaemonConfigPath))
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fail(fmt.Errorf("clear daemon handshake deadline: %w", err))
	}
	return &Connection{Conn: conn, Reader: reader}, true, nil
}

// Pump copies raw MCP bytes in both directions. Daemon EOF is authoritative
// and returns promptly even if stdin remains open. Stdin EOF half-closes the
// socket so the daemon can finish emitting buffered responses.
func Pump(ctx context.Context, connection *Connection, stdin io.Reader, stdout io.Writer) error {
	return pumpWithDrainTimeout(ctx, connection, stdin, stdout, defaultDrainTimeout)
}

func pumpWithDrainTimeout(
	ctx context.Context,
	connection *Connection,
	stdin io.Reader,
	stdout io.Writer,
	drainTimeout time.Duration,
) error {
	if connection == nil || connection.Conn == nil || connection.Reader == nil {
		return fmt.Errorf("invalid daemon shim connection")
	}
	conn := connection.Conn
	outputDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdout, connection.Reader)
		outputDone <- err
	}()
	inputDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(conn, stdin)
		_ = conn.CloseWrite()
		close(inputDone)
	}()

	var drainTimer *time.Timer
	var drainDone <-chan time.Time
	for {
		select {
		case err := <-outputDone:
			if drainTimer != nil {
				drainTimer.Stop()
			}
			_ = conn.Close()
			if err != nil && !errors.Is(err, net.ErrClosed) {
				return fmt.Errorf("copy daemon output: %w", err)
			}
			return nil
		case <-inputDone:
			inputDone = nil
			if drainTimeout <= 0 {
				drainTimeout = defaultDrainTimeout
			}
			drainTimer = time.NewTimer(drainTimeout)
			drainDone = drainTimer.C
		case <-drainDone:
			_ = conn.Close()
			<-outputDone
			return nil
		case <-ctx.Done():
			if drainTimer != nil {
				drainTimer.Stop()
			}
			_ = conn.Close()
			<-outputDone
			return ctx.Err()
		}
	}
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
