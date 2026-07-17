package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/server"
)

const (
	defaultLinger           = 60 * time.Second
	defaultDrainTimeout     = 30 * time.Second
	defaultHandshakeTimeout = 5 * time.Second
	defaultOutboundQueue    = 64
	maxHandshakeLine        = 64 * 1024
)

type Options struct {
	ConfigPath       string
	Build            string
	Version          string
	Revision         string
	Linger           time.Duration
	DrainTimeout     time.Duration
	HandshakeTimeout time.Duration
	OutboundQueue    int
}

type Daemon struct {
	opts       Options
	paths      Paths
	core       *server.Core
	listener   *net.UnixListener
	lifetime   context.Context
	cancelLife context.CancelFunc

	mu       sync.Mutex
	stopping bool
	sessions map[uint64]*liveSession
	nextID   uint64
	idle     *time.Timer

	sessionsWG sync.WaitGroup
	stopOnce   sync.Once
	stopCh     chan struct{}
}

type liveSession struct {
	cancel context.CancelFunc
	conn   net.Conn
}

func normalizeOptions(opts Options) Options {
	if opts.Linger <= 0 {
		opts.Linger = defaultLinger
	}
	if opts.DrainTimeout <= 0 {
		opts.DrainTimeout = defaultDrainTimeout
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = defaultHandshakeTimeout
	}
	if opts.OutboundQueue <= 0 {
		opts.OutboundQueue = defaultOutboundQueue
	}
	return opts
}

func Run(ctx context.Context, opts Options) error {
	opts = normalizeOptions(opts)
	canonical, err := CanonicalConfigPath(opts.ConfigPath)
	if err != nil {
		return err
	}
	opts.ConfigPath = canonical
	paths, err := RuntimePaths(canonical)
	if err != nil {
		return err
	}
	lock, err := acquireFileLock(paths.RunLock)
	if err != nil {
		return err
	}
	defer lock.Close()

	// Owning the run lock proves no live daemon can own this rendezvous path.
	if err := os.Remove(paths.Socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale daemon socket: %w", err)
	}
	listener, err := listenUnix(paths.Socket)
	if err != nil {
		return fmt.Errorf("listen on daemon socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(paths.Socket)
	}()

	executablePath, executableBuild, err := ExecutableIdentity()
	if err != nil {
		return err
	}
	if opts.Build == "" {
		opts.Build = executableBuild
	} else if opts.Build != executableBuild {
		return fmt.Errorf("configured daemon build identity does not match executable")
	}
	record, err := newPIDFile(canonical, executablePath)
	if err != nil {
		return err
	}
	if err := writePIDFile(paths.PIDFile, record); err != nil {
		return err
	}
	defer func() { _ = os.Remove(paths.PIDFile) }()

	cfg, err := config.LoadFrom(canonical)
	if err != nil {
		return fmt.Errorf("load daemon config: %w", err)
	}
	core, err := server.NewCore(server.Options{Config: cfg, ConfigPath: canonical})
	if err != nil {
		return fmt.Errorf("create daemon core: %w", err)
	}
	lifetime, cancelLife := context.WithCancel(context.Background())
	d := &Daemon{
		opts: opts, paths: paths, core: core, listener: listener,
		lifetime: lifetime, cancelLife: cancelLife,
		sessions: make(map[uint64]*liveSession), stopCh: make(chan struct{}),
	}
	core.StartWatching(lifetime)
	defer func() {
		cancelLife()
		core.Close()
	}()
	d.armInitialLinger()

	go func() {
		select {
		case <-ctx.Done():
			d.requestStop()
		case <-d.stopCh:
		}
	}()

	log.Printf("mcpmu daemon listening on %s", paths.Socket)
	for {
		conn, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			d.mu.Lock()
			stopping := d.stopping
			d.mu.Unlock()
			if stopping || errors.Is(acceptErr, net.ErrClosed) {
				break
			}
			if temporary, ok := acceptErr.(interface{ Temporary() bool }); ok && temporary.Temporary() {
				continue
			}
			d.requestStop()
			return fmt.Errorf("accept daemon connection: %w", acceptErr)
		}
		go d.handleConnection(conn)
	}

	d.drain()
	return nil
}

func (d *Daemon) handleConnection(conn *net.UnixConn) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("daemon connection panic: %v", recovered)
			_ = conn.Close()
		}
	}()
	if err := validatePeerUID(conn); err != nil {
		log.Printf("rejecting daemon connection: %v", err)
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Now().Add(d.opts.HandshakeTimeout))
	reader := bufio.NewReaderSize(conn, maxHandshakeLine)
	line, err := readBoundedLine(reader, maxHandshakeLine)
	if err != nil {
		d.writeHandshakeError(conn, fmt.Sprintf("read handshake: %v", err))
		_ = conn.Close()
		return
	}
	var envelope HandshakeEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		d.writeHandshakeError(conn, "malformed handshake")
		_ = conn.Close()
		return
	}
	handshake := envelope.Handshake
	if handshake.ConfigPath != d.opts.ConfigPath {
		d.writeHandshakeError(conn, "config path mismatch")
		_ = conn.Close()
		return
	}
	switch handshake.Type {
	case "session":
		d.handleSession(conn, reader, handshake)
	case "control":
		d.handleControl(conn, reader, handshake)
	default:
		d.writeHandshakeError(conn, "unknown handshake type")
		_ = conn.Close()
	}
}

func (d *Daemon) handleSession(conn *net.UnixConn, reader *bufio.Reader, handshake Handshake) {
	if handshake.Protocol != SessionProtocol {
		d.writeHandshakeError(conn, "session protocol mismatch")
		_ = conn.Close()
		return
	}
	if handshake.Build != d.opts.Build {
		d.writeHandshakeError(conn, "daemon build mismatch")
		_ = conn.Close()
		return
	}
	ctx, cancel := context.WithCancel(d.lifetime)
	id, ok := d.addSession(cancel, conn)
	if !ok {
		cancel()
		d.writeHandshakeError(conn, "daemon is stopping")
		_ = conn.Close()
		return
	}
	defer d.removeSession(id)

	response, _ := marshalLine(HandshakeResponse{OK: true})
	if err := writeAll(conn, response); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	writer := newQueuedWriter(conn, d.opts.OutboundQueue, ctx.Done(), cancel)
	session, err := server.NewSession(d.core, server.Options{
		ConfigPath: d.opts.ConfigPath,
		Namespace:  handshake.Namespace, EagerStart: handshake.Eager,
		ExposeManagerTools: handshake.ExposeManagerTools,
		ExposeResources:    handshake.Resources, ExposePrompts: handshake.Prompts,
		Stdin: reader, Stdout: writer, Stderr: io.Discard,
		ServerName: "mcpmu", ServerVersion: d.opts.Version,
		ProtocolVersion: "2024-11-05",
	})
	if err != nil {
		cancel()
		_ = conn.Close()
		return
	}
	if err := session.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("daemon session ended with error: %v", err)
	}
	flushCtx, flushCancel := context.WithTimeout(context.Background(), time.Second)
	_ = writer.Flush(flushCtx)
	flushCancel()
	cancel()
	_ = conn.Close()
	select {
	case <-writer.exited:
	case <-time.After(time.Second):
	}
}

func (d *Daemon) handleControl(conn *net.UnixConn, reader *bufio.Reader, handshake Handshake) {
	defer func() { _ = conn.Close() }()
	if handshake.ControlProtocol != ControlProtocol {
		d.writeHandshakeError(conn, "control protocol mismatch")
		return
	}
	response, _ := marshalLine(HandshakeResponse{OK: true})
	if err := writeAll(conn, response); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(d.opts.HandshakeTimeout))
	line, err := readBoundedLine(reader, maxControlLine)
	if err != nil {
		return
	}
	var request ControlRequest
	if err := json.Unmarshal(line, &request); err != nil {
		d.writeControl(conn, ControlResponse{Error: "malformed control request"})
		return
	}
	switch request.Command {
	case "status":
		status := d.status()
		d.writeControl(conn, ControlResponse{OK: true, Status: &status})
	case "stop":
		d.writeControl(conn, ControlResponse{OK: true})
		d.requestStop()
	default:
		d.writeControl(conn, ControlResponse{Error: "unknown control command"})
	}
}

func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > limit {
		return nil, fmt.Errorf("line exceeds %d bytes", limit)
	}
	return line, err
}

func (d *Daemon) status() StatusResponse {
	d.mu.Lock()
	sessions := len(d.sessions)
	stopping := d.stopping
	d.mu.Unlock()
	running := d.core.RunningServers()
	slices.Sort(running)
	return StatusResponse{
		Socket: d.paths.Socket, Build: d.opts.Build, Version: d.opts.Version,
		Revision: d.opts.Revision, ConfigPath: d.opts.ConfigPath, PID: os.Getpid(),
		Sessions: sessions, RunningUpstreams: running, Stopping: stopping,
	}
}

func (d *Daemon) writeHandshakeError(conn net.Conn, message string) {
	data, _ := marshalLine(HandshakeResponse{
		Error: message, DaemonBuild: d.opts.Build, DaemonConfigPath: d.opts.ConfigPath,
	})
	_ = writeAll(conn, data)
}

func (d *Daemon) writeControl(conn net.Conn, response ControlResponse) {
	data, _ := marshalLine(response)
	_ = writeAll(conn, data)
}

func (d *Daemon) addSession(cancel context.CancelFunc, conn net.Conn) (uint64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping {
		return 0, false
	}
	if d.idle != nil {
		d.idle.Stop()
		d.idle = nil
	}
	d.nextID++
	id := d.nextID
	d.sessions[id] = &liveSession{cancel: cancel, conn: conn}
	d.sessionsWG.Add(1)
	return id, true
}

func (d *Daemon) removeSession(id uint64) {
	d.mu.Lock()
	delete(d.sessions, id)
	if len(d.sessions) == 0 && !d.stopping {
		d.resetLingerLocked()
	}
	d.mu.Unlock()
	d.sessionsWG.Done()
}

func (d *Daemon) armInitialLinger() {
	d.mu.Lock()
	d.resetLingerLocked()
	d.mu.Unlock()
}

func (d *Daemon) resetLingerLocked() {
	if d.idle != nil {
		d.idle.Stop()
	}
	d.idle = time.AfterFunc(d.opts.Linger, d.requestStop)
}

func (d *Daemon) requestStop() {
	d.stopOnce.Do(func() {
		d.mu.Lock()
		d.stopping = true
		if d.idle != nil {
			d.idle.Stop()
			d.idle = nil
		}
		d.mu.Unlock()
		_ = d.listener.Close()
		close(d.stopCh)
	})
}

func (d *Daemon) drain() {
	done := make(chan struct{})
	go func() {
		d.sessionsWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(d.opts.DrainTimeout):
	}
	d.mu.Lock()
	for _, session := range d.sessions {
		session.cancel()
		_ = session.conn.Close()
	}
	d.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		log.Printf("daemon drain timed out with sessions still active")
	}
}
