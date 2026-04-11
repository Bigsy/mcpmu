package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/web"
	"github.com/spf13/cobra"
)

var (
	webAddr  string
	webDebug bool
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Start the browser-based management UI",
	Long: `Start an HTTP server with a browser-based UI for managing MCP server configurations.

The web UI provides:
  - Server list with status, tools, and log streaming
  - Namespace list with tool permissions
  - Real-time log viewer via Server-Sent Events

By default, binds to 127.0.0.1:8080 (localhost only).`,
	RunE: runWeb,
}

func init() {
	webCmd.Flags().StringVar(&webAddr, "addr", "127.0.0.1:8080", "Listen address (host:port)")
	webCmd.Flags().BoolVar(&webDebug, "debug", false, "Enable debug logging to /tmp/mcpmu-debug.log")
	rootCmd.AddCommand(webCmd)
}

func runWeb(cmd *cobra.Command, args []string) error {
	// Set up debug logging
	if webDebug {
		logFile, err := os.OpenFile("/tmp/mcpmu-debug.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err == nil {
			log.SetOutput(logFile)
			log.SetFlags(log.LstdFlags | log.Lshortfile)
			defer func() { _ = logFile.Close() }()
			log.Println("=== mcpmu web starting (debug mode) ===")
		}
	} else {
		log.SetOutput(io.Discard)
	}

	// Acquire manager lock
	mgrLock, err := process.NewManagerLock(configPath)
	if err != nil {
		return fmt.Errorf("failed to create manager lock: %w", err)
	}
	if err := mgrLock.Acquire("web"); err != nil {
		return err
	}
	defer mgrLock.Release()

	// Load configuration
	var cfg *config.Config
	if configPath != "" {
		cfg, err = config.LoadFrom(configPath)
	} else {
		cfg, err = config.Load()
	}
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

	// Create tool cache
	toolCache, err := config.NewToolCache(configPath)
	if err != nil {
		log.Printf("Warning: failed to create tool cache: %v", err)
	}

	// Create process supervisor
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode:     cfg.MCPOAuthCredentialStore,
		GlobalOAuthCallbackPort: cfg.MCPOAuthCallbackPort,
	})
	supervisor.SetToolCache(toolCache)

	// Create web server
	srv, err := web.New(web.Options{
		Addr:       webAddr,
		Config:     cfg,
		ConfigPath: configPath,
		Supervisor: supervisor,
		Bus:        bus,
		ToolCache:  toolCache,
	})
	if err != nil {
		return fmt.Errorf("failed to create web server: %w", err)
	}

	// Start autostart servers
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for name, srvCfg := range cfg.Servers {
		if srvCfg.Autostart && srvCfg.IsEnabled() {
			log.Printf("Autostarting server: %s", name)
			if _, err := supervisor.Start(ctx, name, srvCfg); err != nil {
				log.Printf("Warning: failed to autostart %s: %v", name, err)
			}
		}
	}

	// Graceful shutdown on signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start HTTP server in background
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	fmt.Fprintf(os.Stderr, "mcpmu web listening on http://%s\n", webAddr)

	// Wait for signal or server error
	select {
	case sig := <-sigCh:
		log.Printf("Received signal %v, initiating graceful shutdown", sig)
		fmt.Fprintln(os.Stderr, "\nShutting down...")
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server error: %w", err)
		}
	}

	// Stop signal handling
	signal.Stop(sigCh)

	// Graceful HTTP shutdown (5s timeout)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*1e9)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}

	// Stop all managed servers
	log.Println("Stopping all servers...")
	supervisor.StopAll()

	log.Println("=== mcpmu web exiting ===")
	return nil
}
