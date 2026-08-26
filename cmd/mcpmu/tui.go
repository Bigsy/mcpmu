package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Bigsy/mcpmu/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

var tuiDebug bool

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Run the interactive terminal UI",
	Long: `Run mcpmu in interactive TUI mode for managing MCP server configurations.

Use this for:
  - Adding, editing, and removing server configurations
  - Starting/stopping servers manually
  - Viewing server logs and status
  - Managing namespaces and tool permissions`,
	RunE: runTUI,
}

func init() {
	tuiCmd.Flags().BoolVar(&tuiDebug, "debug", false, "Enable debug logging to /tmp/mcpmu-debug.log")
	rootCmd.AddCommand(tuiCmd)
}

func runTUI(cmd *cobra.Command, args []string) error {
	rt, err := startManager("tui", tuiDebug)
	if err != nil {
		return err
	}
	defer rt.Close()

	model := tui.NewModel(rt.cfg, rt.supervisor, rt.bus, rt.configPath, rt.toolCache)

	// Set up signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	p := tea.NewProgram(model, tea.WithAltScreen())
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, initiating graceful shutdown", sig)
		p.Quit()
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	// Note: We intentionally do NOT save config on close.
	// All config changes are saved immediately when made (in model.go).
	// Saving on close would risk a stale TUI overwriting changes made elsewhere.

	log.Println("=== mcpmu TUI exiting ===")
	return nil
}
