package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/server"
	"github.com/spf13/cobra"
)

var (
	serveConfigPath         string
	serveNamespace          string
	serveLogLevel           string
	serveEager              bool
	serveExposeManagerTools bool
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

	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	// In stdio mode, all output must go to stderr except MCP protocol
	// Configure logging based on log level
	switch serveLogLevel {
	case "debug":
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	case "info", "warn":
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	case "error":
		// Only log errors
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	default:
		log.SetOutput(io.Discard)
	}

	log.Printf("mcpmu serve starting (version=%s)", version)

	// Resolve config path for hot-reload watching
	var resolvedConfigPath string
	if serveConfigPath != "" {
		// Expand ~ in user-provided path
		if strings.HasPrefix(serveConfigPath, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home dir: %w", err)
			}
			resolvedConfigPath = filepath.Join(home, serveConfigPath[2:])
		} else {
			resolvedConfigPath = serveConfigPath
		}
	} else {
		// Use default config path
		var err error
		resolvedConfigPath, err = config.ConfigPath()
		if err != nil {
			return fmt.Errorf("failed to get config path: %w", err)
		}
	}

	// Load configuration
	cfg, err := config.LoadFrom(resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	log.Printf("Loaded config with %d servers, %d namespaces", len(cfg.Servers), len(cfg.Namespaces))

	// Create server options
	opts := server.Options{
		Config:             cfg,
		ConfigPath:         resolvedConfigPath, // For hot-reload watching
		Namespace:          serveNamespace,
		EagerStart:         serveEager,
		ExposeManagerTools: serveExposeManagerTools,
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

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down", sig)
		cancel()
	}()

	// Run the server
	if err := srv.Run(ctx); err != nil && err != context.Canceled {
		return fmt.Errorf("server error: %w", err)
	}

	log.Println("mcpmu serve exiting")
	return nil
}
