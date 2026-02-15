package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version information (set at build time via ldflags)
var (
	version = "dev"
	commit  = "unknown"
)

// configPath is the custom config file path (empty for default)
var configPath string

var rootCmd = &cobra.Command{
	Use:   "mcpmu",
	Short: "MCP server aggregator and manager",
	Long: `mcpmu aggregates multiple MCP servers into a single interface.

Running without a subcommand starts the interactive TUI.
Use 'mcpmu serve --stdio' to run as an MCP server (spawned by Claude Code).`,
	Version: fmt.Sprintf("%s (commit: %s)", version, commit),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Default to TUI when no subcommand is given
		return runTUI(cmd, args)
	},
}

func init() {
	// Suppress errors from being printed twice
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true

	// Add persistent --config flag (available to all subcommands)
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "",
		"Path to config file (default: ~/.config/mcpmu/config.json)")

	// Add --debug flag to root command (for default TUI mode)
	rootCmd.Flags().BoolVar(&tuiDebug, "debug", false, "Enable debug logging to /tmp/mcpmu-debug.log")
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
