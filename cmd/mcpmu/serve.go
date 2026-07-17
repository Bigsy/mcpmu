package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/daemon"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/server"
	"github.com/Bigsy/mcpmu/internal/shim"
	"github.com/spf13/cobra"
)

var (
	serveConfigPath         string
	serveNamespace          string
	serveLogLevel           string
	serveEager              bool
	serveExposeManagerTools bool
	serveResources          bool
	servePrompts            bool
	serveIsolated           bool
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run as an MCP server",
	Long: `Run mcpmu as an MCP server that aggregates tools from configured upstream servers.

This mode is intended to be spawned by Claude Code or other MCP clients.
Configure in Claude Code's mcp_servers.json:

  {
    "mcpmu": {
      "command": "mcpmu",
      "args": ["serve", "--stdio", "--namespace", "work"]
    }
  }

Tool names are prefixed with the server ID (e.g., filesystem.read_file).
Manager tools (mcpmu.servers_list, etc.) are hidden by default but remain
callable. Use --expose-manager-tools to include them in tools/list.`,
	RunE: runServe,
}

func init() {
	// --stdio is a no-op flag for compatibility (stdio is the only transport for now)
	serveCmd.Flags().Bool("stdio", false, "Use stdio transport (default, always enabled)")
	_ = serveCmd.Flags().MarkHidden("stdio")

	serveCmd.Flags().StringVarP(&serveConfigPath, "config", "c", "", "Path to config file (default: ~/.config/mcpmu/config.json)")
	serveCmd.Flags().StringVarP(&serveNamespace, "namespace", "n", "", "Namespace to expose (default: auto-select)")
	serveCmd.Flags().StringVarP(&serveLogLevel, "log-level", "l", "info", "Log level (debug, info, warn, error)")
	serveCmd.Flags().BoolVar(&serveEager, "eager", false, "Pre-start all servers on init (default: lazy start)")
	serveCmd.Flags().BoolVar(&serveExposeManagerTools, "expose-manager-tools", false, "Include mcpmu.* tools in tools/list (default: hidden)")
	serveCmd.Flags().BoolVar(&serveResources, "resources", true, "Passthrough resources/* from upstream servers")
	serveCmd.Flags().BoolVar(&servePrompts, "prompts", true, "Passthrough prompts/* from upstream servers")
	serveCmd.Flags().BoolVar(&serveIsolated, "isolated", false, "Run embedded with private upstream server instances")

	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	// In stdio mode, all output must go to stderr except MCP protocol
	// Configure logging based on log level
	switch serveLogLevel {
	case "debug":
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags | log.Lshortfile)
		server.DebugLogging = true
		mcp.DebugLogging = true
	case "info", "warn":
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	case "error":
		log.SetOutput(io.Discard)
	default:
		log.SetOutput(io.Discard)
	}

	log.Printf("mcpmu serve starting (version=%s)", version)

	// Resolve and canonicalize the config path once for embedded watching and
	// daemon rendezvous identity.
	var resolvedConfigPath string
	if serveConfigPath != "" {
		resolvedConfigPath = serveConfigPath
	} else if configPath != "" {
		resolvedConfigPath = configPath
	} else {
		// Use default config path
		var err error
		resolvedConfigPath, err = config.ConfigPath()
		if err != nil {
			return fmt.Errorf("failed to get config path: %w", err)
		}
	}
	resolvedConfigPath, err := daemon.CanonicalConfigPath(resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	// Load configuration
	cfg, err := config.LoadFrom(resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	log.Printf("Loaded config with %d servers, %d namespaces", len(cfg.Servers), len(cfg.Namespaces))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case sig := <-sigCh:
			log.Printf("Received signal %v, shutting down", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	// Windows has no daemon transport yet; use embedded mode directly instead
	// of treating the expected platform limitation as a fallback failure.
	if runtime.GOOS != "windows" && cfg.IsDaemonModeEnabled() && !serveIsolated {
		connection, connectErr := shim.ConnectOrSpawn(ctx, shim.Options{
			ConfigPath: resolvedConfigPath, Namespace: serveNamespace,
			LogLevel: serveLogLevel, Eager: serveEager,
			ExposeManagerTools: serveExposeManagerTools,
			Resources:          serveResources, Prompts: servePrompts,
		})
		if connectErr == nil {
			if err := shim.Pump(ctx, connection, os.Stdin, os.Stdout); err != nil && err != context.Canceled {
				return fmt.Errorf("daemon shim error: %w", err)
			}
			log.Println("mcpmu serve shim exiting")
			return nil
		}
		_, _ = fmt.Fprintf(os.Stderr, "mcpmu: shared daemon unavailable; falling back to embedded serve: %v\n", connectErr)
	}

	return runEmbeddedServe(ctx, cfg, resolvedConfigPath)
}

func runEmbeddedServe(ctx context.Context, cfg *config.Config, resolvedConfigPath string) error {
	// Create server options
	opts := server.Options{
		Config:             cfg,
		ConfigPath:         resolvedConfigPath, // For hot-reload watching
		Namespace:          serveNamespace,
		EagerStart:         serveEager,
		ExposeManagerTools: serveExposeManagerTools,
		ExposeResources:    serveResources,
		ExposePrompts:      servePrompts,
		LogLevel:           serveLogLevel,
		Stdin:              os.Stdin,
		Stdout:             os.Stdout,
		Stderr:             os.Stderr,
		ServerName:         "mcpmu",
		ServerVersion:      version,
		ProtocolVersion:    "2024-11-05",
	}

	// Create and run server
	srv, err := server.New(opts)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	// Run the server
	if err := srv.Run(ctx); err != nil && err != context.Canceled {
		return fmt.Errorf("server error: %w", err)
	}

	log.Println("mcpmu serve exiting")
	return nil
}
