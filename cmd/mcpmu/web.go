package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Bigsy/mcpmu/internal/web"
	"github.com/spf13/cobra"
)

var (
	webAddr        string
	webDebug       bool
	webToken       string
	webAllowOrigin []string
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
	webCmd.Flags().StringVar(&webToken, "token", "", "Auth token for web UI (or set MCPMU_WEB_TOKEN)")
	webCmd.Flags().StringArrayVar(&webAllowOrigin, "allow-origin", nil, "Extra allowed Origin, e.g. behind a reverse proxy under another host (repeatable; also satisfies the Host check for forwarded Host headers)")
	rootCmd.AddCommand(webCmd)
}

func runWeb(cmd *cobra.Command, args []string) error {
	rt, err := startManager("web", webDebug)
	if err != nil {
		return err
	}
	defer rt.Close()

	// Resolve auth token: --token flag takes precedence over env var
	token := webToken
	if token == "" {
		token = os.Getenv("MCPMU_WEB_TOKEN")
	}

	srv, err := web.New(web.Options{
		Addr:           webAddr,
		Config:         rt.cfg,
		ConfigPath:     rt.configPath,
		Supervisor:     rt.supervisor,
		Bus:            rt.bus,
		ToolCache:      rt.toolCache,
		Token:          token,
		AllowedOrigins: webAllowOrigin,
	})
	if err != nil {
		return fmt.Errorf("failed to create web server: %w", err)
	}

	// Start config file watcher and autostart servers
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.WatchConfig(ctx)

	for name, srvCfg := range rt.cfg.Servers {
		if srvCfg.Autostart && srvCfg.IsEnabled() {
			log.Printf("Autostarting server: %s", name)
			if _, err := rt.supervisor.Start(ctx, name, srvCfg); err != nil {
				log.Printf("Warning: failed to autostart %s: %v", name, err)
			}
		}
	}

	// Graceful shutdown on signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), httpShutdownGrace)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}

	log.Println("=== mcpmu web exiting ===")
	return nil
}
