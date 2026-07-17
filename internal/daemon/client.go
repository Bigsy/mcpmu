package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

const defaultControlTimeout = 5 * time.Second

// Control sends one versioned command to the daemon for configPath. Control
// connections intentionally do not carry or enforce the caller's build
// identity, so a newly installed binary can still inspect or stop an older
// daemon.
func Control(ctx context.Context, configPath, command string) (ControlResponse, error) {
	canonical, err := CanonicalConfigPath(configPath)
	if err != nil {
		return ControlResponse{}, err
	}
	paths, err := RuntimePaths(canonical)
	if err != nil {
		return ControlResponse{}, err
	}

	dialer := net.Dialer{}
	rawConn, err := dialer.DialContext(ctx, "unix", paths.Socket)
	if err != nil {
		return ControlResponse{}, fmt.Errorf("connect to daemon: %w", err)
	}
	conn, ok := rawConn.(*net.UnixConn)
	if !ok {
		_ = rawConn.Close()
		return ControlResponse{}, fmt.Errorf("daemon connection is not a unix socket")
	}
	defer func() { _ = conn.Close() }()
	cancelWatch := make(chan struct{})
	defer close(cancelWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-cancelWatch:
		}
	}()

	deadline := time.Now().Add(defaultControlTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return ControlResponse{}, fmt.Errorf("set daemon control deadline: %w", err)
	}

	handshake, err := marshalLine(HandshakeEnvelope{Handshake: Handshake{
		Type:            "control",
		ControlProtocol: ControlProtocol,
		ConfigPath:      canonical,
		PID:             os.Getpid(),
	}})
	if err != nil {
		return ControlResponse{}, err
	}
	if err := writeAll(conn, handshake); err != nil {
		return ControlResponse{}, fmt.Errorf("write daemon control handshake: %w", err)
	}

	reader := bufio.NewReaderSize(conn, maxControlLine)
	line, err := readBoundedLine(reader, maxControlLine)
	if err != nil {
		return ControlResponse{}, fmt.Errorf("read daemon control handshake: %w", err)
	}
	var accepted HandshakeResponse
	if err := json.Unmarshal(line, &accepted); err != nil {
		return ControlResponse{}, fmt.Errorf("parse daemon control handshake: %w", err)
	}
	if !accepted.OK {
		if accepted.Error == "" {
			accepted.Error = "daemon rejected control connection"
		}
		return ControlResponse{}, errors.New(accepted.Error)
	}

	request, err := marshalLine(ControlRequest{Command: command})
	if err != nil {
		return ControlResponse{}, err
	}
	if err := writeAll(conn, request); err != nil {
		return ControlResponse{}, fmt.Errorf("write daemon control request: %w", err)
	}
	line, err = readBoundedLine(reader, maxControlLine)
	if err != nil {
		return ControlResponse{}, fmt.Errorf("read daemon control response: %w", err)
	}
	var response ControlResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return ControlResponse{}, fmt.Errorf("parse daemon control response: %w", err)
	}
	if !response.OK {
		if response.Error == "" {
			response.Error = "daemon control command failed"
		}
		return response, errors.New(response.Error)
	}
	return response, nil
}

// Inspect returns live daemon status. If the control socket is unavailable,
// it validates the pidfile against the requested full config path and current
// process identity and returns a deliberately limited status snapshot.
func Inspect(ctx context.Context, configPath string) (status StatusResponse, pidfileFallback bool, err error) {
	response, controlErr := Control(ctx, configPath, "status")
	if controlErr == nil {
		if response.Status == nil {
			return StatusResponse{}, false, fmt.Errorf("daemon returned no status")
		}
		return *response.Status, false, nil
	}

	canonical, paths, record, fallbackErr := validatedPIDRecord(configPath)
	if fallbackErr != nil {
		return StatusResponse{}, false, fmt.Errorf("daemon control failed: %v; pidfile fallback failed: %w", controlErr, fallbackErr)
	}
	return StatusResponse{
		Socket: paths.Socket, ConfigPath: canonical, PID: record.PID,
	}, true, nil
}

// Stop asks the daemon to drain over its control connection. If the socket is
// unavailable or the control handshake fails, it signals only after validating
// every pidfile identity field.
func Stop(ctx context.Context, configPath string) (pidfileFallback bool, err error) {
	if _, controlErr := Control(ctx, configPath, "stop"); controlErr == nil {
		return false, nil
	} else {
		_, _, record, fallbackErr := validatedPIDRecord(configPath)
		if fallbackErr != nil {
			return false, fmt.Errorf("daemon control failed: %v; pidfile fallback failed: %w", controlErr, fallbackErr)
		}
		if err := signalDaemon(record.PID); err != nil {
			return false, fmt.Errorf("signal validated daemon PID %d: %w", record.PID, err)
		}
		return true, nil
	}
}

func validatedPIDRecord(configPath string) (string, Paths, PIDFile, error) {
	canonical, err := CanonicalConfigPath(configPath)
	if err != nil {
		return "", Paths{}, PIDFile{}, err
	}
	paths, err := RuntimePaths(canonical)
	if err != nil {
		return "", Paths{}, PIDFile{}, err
	}
	record, err := ReadPIDFile(paths.PIDFile)
	if err != nil {
		return "", Paths{}, PIDFile{}, err
	}
	if err := ValidatePIDFile(record, canonical); err != nil {
		return "", Paths{}, PIDFile{}, err
	}
	return canonical, paths, record, nil
}
