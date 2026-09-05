package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/daemon"
	"github.com/spf13/cobra"
)

type diagnosticCheck struct {
	Server string `json:"server,omitempty"`
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	OK     bool   `json:"ok"`
}
type diagnosticReport struct {
	ConfigPath      string                 `json:"configPath"`
	DefaultsUsed    bool                   `json:"defaultsUsed"`
	ConfigValid     bool                   `json:"configValid"`
	Scope           string                 `json:"scope"`
	DaemonEnabled   bool                   `json:"daemonEnabled"`
	DaemonState     string                 `json:"daemonState"`
	BuildCompatible *bool                  `json:"buildCompatible,omitempty"`
	Daemon          *daemon.StatusResponse `json:"daemon,omitempty"`
	Checks          []diagnosticCheck      `json:"checks"`
}

func init() {
	for _, kind := range []string{"status", "doctor"} {
		cmd := &cobra.Command{Use: kind, Short: map[string]string{"status": "Show config and shared daemon status (read-only)", "doctor": "Check local configuration and prerequisites (read-only)"}[kind], Args: cobra.NoArgs}
		cmd.Flags().Bool("json", false, "Output diagnostics as JSON")
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			report, err := collectDiagnostics(cmd.Context(), kind == "doctor")
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				if e := json.NewEncoder(cmd.OutOrStdout()).Encode(report); e != nil {
					return e
				}
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Config: %s (valid: %t, defaults used: %t)\nDaemon: %s (enabled: %t)\nScope: %s\n", report.ConfigPath, report.ConfigValid, report.DefaultsUsed, report.DaemonState, report.DaemonEnabled, report.Scope)
				if report.Daemon != nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "PID: %d; sessions: %d; running instances: %s\n", report.Daemon.PID, report.Daemon.Sessions, strings.Join(report.Daemon.RunningUpstreams, ", "))
				}
				for _, c := range report.Checks {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s (ok: %t)\n", c.Server, c.Kind, c.Name, c.OK)
				}
			}
			return err
		}
		rootCmd.AddCommand(cmd)
	}
}

func collectDiagnostics(ctx context.Context, doctor bool) (diagnosticReport, error) {
	r := diagnosticReport{Scope: "Current CLI environment plus per-server overrides; the daemon may have inherited a different environment.", DaemonState: "unavailable", Checks: []diagnosticCheck{}}
	path, err := daemonConfigPath(false)
	if err != nil {
		return r, errors.New("cannot resolve config path")
	}
	r.ConfigPath = path
	_, statErr := os.Stat(path)
	r.DefaultsUsed = os.IsNotExist(statErr)
	cfg, err := config.LoadFrom(path)
	if err != nil {
		return r, errors.New("configuration is invalid or unreadable; check the config file")
	}
	r.ConfigValid = true
	r.DaemonEnabled = cfg.IsDaemonModeEnabled()
	if runtime.GOOS == "windows" {
		r.DaemonState = "unsupported"
	} else {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		status, fallback, inspectErr := daemon.Inspect(bounded, path)
		if inspectErr == nil {
			r.Daemon = &status
			r.DaemonState = "running"
			if fallback {
				r.DaemonState = "control_unavailable"
			} else if _, build, e := daemon.ExecutableIdentity(); e == nil {
				compatible := build == status.Build
				r.BuildCompatible = &compatible
				if !compatible {
					r.DaemonState = "incompatible"
				}
			}
		} else if paths, e := daemon.ExistingRuntimePaths(path); e == nil {
			_, socketErr := os.Stat(paths.Socket)
			_, pidErr := os.Stat(paths.PIDFile)
			if os.IsNotExist(socketErr) && os.IsNotExist(pidErr) {
				r.DaemonState = "not_running"
			}
		}
		if !r.DaemonEnabled && r.DaemonState == "not_running" {
			r.DaemonState = "disabled"
		}
	}
	if !doctor {
		return r, nil
	}
	failed := false
	for _, entry := range cfg.ServerEntries() {
		srv := entry.Config
		if !srv.IsEnabled() {
			continue
		}
		check := func(kind, name string, ok bool) {
			r.Checks = append(r.Checks, diagnosticCheck{entry.Name, kind, name, ok})
			failed = failed || !ok
		}
		env := func(name string) (string, bool) {
			if value, ok := srv.Env[name]; ok {
				return value, value != ""
			}
			value, ok := os.LookupEnv(name)
			return value, ok && value != ""
		}
		cwd := srv.Cwd
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		if !srv.IsHTTP() {
			info, e := os.Stat(cwd)
			check("working_directory", "", e == nil && info.IsDir())
			searchPath, _ := env("PATH")
			check("executable", "", diagnosticExecutable(srv.Command, cwd, searchPath))
		}
		names := []string{}
		if srv.BearerTokenEnvVar != "" {
			names = append(names, srv.BearerTokenEnvVar)
		}
		for _, name := range srv.EnvHTTPHeaders {
			names = append(names, name)
		}
		slices.Sort(names)
		names = slices.Compact(names)
		for _, name := range names {
			_, ok := env(name)
			check("environment", name, ok)
		}
	}
	if failed {
		return r, errors.New("local prerequisite checks failed; correct the failed checks above")
	}
	return r, nil
}

// Check executable permission without running a command or changing process cwd.
func diagnosticExecutable(command, cwd, searchPath string) bool {
	executable := func(path string) bool {
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		info, err := os.Stat(path)
		return err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0111 != 0)
	}
	if strings.ContainsAny(command, "/\\") {
		return executable(command)
	}
	for _, dir := range filepath.SplitList(searchPath) {
		if executable(filepath.Join(dir, command)) {
			return true
		}
	}
	return false
}
