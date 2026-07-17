package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Bigsy/mcpmu/internal/process"
)

type PIDFile struct {
	ConfigPath           string    `json:"configPath"`
	PID                  int       `json:"pid"`
	StartedAt            time.Time `json:"startedAt"`
	ProcessStartIdentity int64     `json:"processStartIdentity"`
	ExecutablePath       string    `json:"executablePath"`
}

func newPIDFile(configPath, executablePath string) (PIDFile, error) {
	identity, err := process.ProcessStartIdentity(os.Getpid())
	if err != nil {
		return PIDFile{}, fmt.Errorf("identify daemon process: %w", err)
	}
	return PIDFile{
		ConfigPath:           configPath,
		PID:                  os.Getpid(),
		StartedAt:            time.Now().UTC(),
		ProcessStartIdentity: identity,
		ExecutablePath:       executablePath,
	}, nil
}

func writePIDFile(path string, record PIDFile) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal daemon pidfile: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create daemon pidfile: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure daemon pidfile: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write daemon pidfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync daemon pidfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close daemon pidfile: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install daemon pidfile: %w", err)
	}
	return nil
}

func ReadPIDFile(path string) (PIDFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PIDFile{}, fmt.Errorf("read daemon pidfile: %w", err)
	}
	var record PIDFile
	if err := json.Unmarshal(data, &record); err != nil {
		return PIDFile{}, fmt.Errorf("parse daemon pidfile: %w", err)
	}
	return record, nil
}

// ValidatePIDFile checks every persisted identity field against the requested
// config and the currently running process. A failure is conservative: the
// caller must not signal the recorded PID.
func ValidatePIDFile(record PIDFile, expectedConfigPath string) error {
	if record.ConfigPath != expectedConfigPath {
		return fmt.Errorf("pidfile config path mismatch: got %q, want %q", record.ConfigPath, expectedConfigPath)
	}
	if record.PID <= 1 || record.ProcessStartIdentity == 0 || record.ExecutablePath == "" || record.StartedAt.IsZero() {
		return fmt.Errorf("pidfile is missing required process identity fields")
	}
	identity, err := process.ProcessStartIdentity(record.PID)
	if err != nil {
		return fmt.Errorf("verify daemon PID %d: %w", record.PID, err)
	}
	if identity != record.ProcessStartIdentity {
		return fmt.Errorf("daemon PID %d was reused", record.PID)
	}
	executable, err := processExecutablePath(record.PID)
	if err != nil {
		return fmt.Errorf("verify daemon executable: %w", err)
	}
	recordedExecutable, err := filepath.EvalSymlinks(record.ExecutablePath)
	if err != nil {
		return fmt.Errorf("resolve recorded daemon executable: %w", err)
	}
	if executable != recordedExecutable {
		return fmt.Errorf("daemon executable mismatch: got %q, want %q", executable, recordedExecutable)
	}
	return nil
}
