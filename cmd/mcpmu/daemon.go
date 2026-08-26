package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/daemon"
	"github.com/spf13/cobra"
)

var (
	daemonForeground bool
	daemonLogLevel   string
	daemonStatusJSON bool
)

var daemonCmd = &cobra.Command{
	Use:    "daemon",
	Short:  "Manage the shared MCP daemon",
	Hidden: true,
}

var daemonRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the shared MCP daemon",
	Args:  cobra.NoArgs,
	RunE:  runDaemon,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show shared daemon status",
	Args:  cobra.NoArgs,
	RunE:  runDaemonStatus,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Gracefully stop the shared daemon",
	Args:  cobra.NoArgs,
	RunE:  runDaemonStop,
}

func init() {
	daemonRunCmd.Flags().BoolVar(&daemonForeground, "foreground", false, "Log to stderr instead of the per-config daemon log")
	daemonRunCmd.Flags().StringVar(&daemonLogLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	daemonStatusCmd.Flags().BoolVar(&daemonStatusJSON, "json", false, "Output status as JSON")

	daemonCmd.AddCommand(daemonRunCmd, daemonStatusCmd, daemonStopCmd)
	rootCmd.AddCommand(daemonCmd)
}

func daemonConfigPath(requireExplicit bool) (string, error) {
	path := configPath
	if path == "" {
		if requireExplicit {
			return "", fmt.Errorf("daemon run requires --config")
		}
		var err error
		path, err = config.ConfigPath()
		if err != nil {
			return "", fmt.Errorf("get default config path: %w", err)
		}
	}
	canonical, err := daemon.CanonicalConfigPath(path)
	if err != nil {
		return "", err
	}
	return canonical, nil
}

func runDaemon(_ *cobra.Command, _ []string) error {
	if _, err := parseLogLevel(daemonLogLevel); err != nil {
		return err
	}
	canonical, err := daemonConfigPath(true)
	if err != nil {
		return err
	}
	paths, err := daemon.RuntimePaths(canonical)
	if err != nil {
		return err
	}

	var logFile *os.File
	output := io.Writer(os.Stderr)
	if !daemonForeground {
		logFile, err = os.OpenFile(paths.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("open daemon log: %w", err)
		}
		defer func() { _ = logFile.Close() }()
		output = logFile
	}
	if err := configureLogging(daemonLogLevel, output); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("mcpmu daemon starting (version=%s, revision=%s, config=%s)", version, commit, canonical)
	return daemon.Run(ctx, daemon.Options{
		ConfigPath: canonical,
		Version:    version,
		Revision:   commit,
	})
}

func runDaemonStatus(_ *cobra.Command, _ []string) error {
	path, err := daemonConfigPath(false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, fallback, err := daemon.Inspect(ctx, path)
	if err != nil {
		return err
	}
	if daemonStatusJSON {
		payload := struct {
			daemon.StatusResponse
			PIDFileFallback bool `json:"pidfileFallback"`
		}{StatusResponse: status, PIDFileFallback: fallback}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}

	fmt.Printf("Config: %s\n", status.ConfigPath)
	fmt.Printf("Socket: %s\n", status.Socket)
	fmt.Printf("PID: %d\n", status.PID)
	if fallback {
		fmt.Println("Control: unavailable (identity-validated pidfile fallback)")
		return nil
	}
	fmt.Printf("Version: %s\n", status.Version)
	fmt.Printf("Revision: %s\n", status.Revision)
	fmt.Printf("Build: %s\n", status.Build)
	fmt.Printf("Sessions: %d\n", status.Sessions)
	fmt.Printf("Running upstreams: %d\n", len(status.RunningUpstreams))
	fmt.Printf("Stopping: %t\n", status.Stopping)
	return nil
}

func runDaemonStop(_ *cobra.Command, _ []string) error {
	path, err := daemonConfigPath(false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fallback, err := daemon.Stop(ctx, path)
	if err != nil {
		return err
	}
	if fallback {
		fmt.Println("Daemon signalled using identity-validated pidfile fallback")
	} else {
		fmt.Println("Daemon stopping")
	}
	return nil
}
