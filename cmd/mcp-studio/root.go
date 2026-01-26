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

var rootCmd = &cobra.Command{
	Use:   "mcp-studio",
	Short: "MCP server aggregator and manager",
	Long: `mcp-studio aggregates multiple MCP servers into a single interface.

Running without a subcommand starts the interactive TUI.
Use 'mcp-studio serve --stdio' to run as an MCP server (spawned by Claude Code).`,
	Version: fmt.Sprintf("%s (commit: %s)", version, commit),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Default to TUI when no subcommand is given
		return runTUI(cmd, args)
	},
}

func init() {
	// Disable automatic completion command
	rootCmd.CompletionOptions.DisableDefaultCmd = true

	// Suppress errors from being printed twice
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true

	// Add --debug flag to root command (for default TUI mode)
	rootCmd.Flags().BoolVar(&tuiDebug, "debug", false, "Enable debug logging to /tmp/mcp-studio-debug.log")
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
