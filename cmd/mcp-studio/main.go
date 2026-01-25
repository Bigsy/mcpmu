package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/events"
	"github.com/hedworth/mcp-studio-go/internal/process"
	"github.com/hedworth/mcp-studio-go/internal/tui"
)

var debugMode bool

func main() {
	flag.BoolVar(&debugMode, "debug", false, "Enable debug logging to /tmp/mcp-studio-debug.log")
	flag.Parse()

	// Set up debug logging to file if enabled
	if debugMode {
		logFile, err := os.OpenFile("/tmp/mcp-studio-debug.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err == nil {
			log.SetOutput(logFile)
			log.SetFlags(log.LstdFlags | log.Lshortfile)
			defer logFile.Close()
			log.Println("=== MCP Studio starting (debug mode) ===")
		}
	} else {
		// Discard logs when not in debug mode
		log.SetOutput(io.Discard)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
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

	// Create process supervisor
	supervisor := process.NewSupervisor(bus)

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
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}

	// Stop signal handling
	signal.Stop(sigCh)

	// Ensure all servers are stopped
	log.Println("Stopping all servers...")
	supervisor.StopAll()

	// Save config
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save config: %v\n", err)
	}

	log.Println("=== MCP Studio exiting ===")
}
