package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/tui"
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
	// Set up debug logging to file if enabled
	if tuiDebug {
		logFile, err := os.OpenFile("/tmp/mcpmu-debug.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err == nil {
			log.SetOutput(logFile)
			log.SetFlags(log.LstdFlags | log.Lshortfile)
			defer logFile.Close()
			log.Println("=== mcpmu TUI starting (debug mode) ===")
		}
	} else {
		// Discard logs when not in debug mode
		log.SetOutput(io.Discard)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	log.Printf("Loaded %d servers from config", len(cfg.Servers))

	// Create event bus
	bus := events.NewBus()
	defer bus.Close()

	// Log all events to debug file
	bus.Subscribe(func(e events.Event) {
		switch evt := e.(type) {
		case events.StatusChangedEvent:
			log.Printf("EVENT StatusChanged: server=%s old=%s new=%s err=%s",
				evt.ServerID(), evt.OldState, evt.NewState, evt.Status.Error)
		case events.LogReceivedEvent:
			log.Printf("EVENT Log: server=%s line=%s", evt.ServerID(), evt.Line)
		case events.ErrorEvent:
			log.Printf("EVENT Error: server=%s msg=%s err=%v", evt.ServerID(), evt.Message, evt.Err)
		case events.ToolsUpdatedEvent:
			log.Printf("EVENT ToolsUpdated: server=%s count=%d", evt.ServerID(), len(evt.Tools))
		}
	})

	// Create process supervisor with config-specified credential store
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode: cfg.MCPOAuthCredentialStore,
	})

	// Create TUI model
	model := tui.NewModel(cfg, supervisor, bus)

	// Set up signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Run Bubble Tea program
	p := tea.NewProgram(model, tea.WithAltScreen())

	// Handle signals in background
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, initiating graceful shutdown", sig)
		p.Quit()
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	// Stop signal handling
	signal.Stop(sigCh)

	// Ensure all servers are stopped
	log.Println("Stopping all servers...")
	supervisor.StopAll()

	// Note: We intentionally do NOT save config on close.
	// All config changes are saved immediately when made (in model.go).
	// Saving on close would risk a stale TUI overwriting changes made elsewhere.

	log.Println("=== mcpmu TUI exiting ===")
	return nil
}
