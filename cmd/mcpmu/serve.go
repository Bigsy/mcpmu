package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/httpserve"
	"github.com/Bigsy/mcpmu/internal/server"
	"github.com/Bigsy/mcpmu/internal/shim"
	"github.com/spf13/cobra"
)

var (
	serveNamespace          string
	serveLogLevel           string
	serveEager              bool
	serveExposeManagerTools bool
	serveResources          bool
	servePrompts            bool
	serveIsolated           bool

	serveHTTP               bool
	serveAddr               string
	serveToken              string
	serveAllowOrigins       []string
	serveSessionIdleTimeout time.Duration
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

	serveCmd.Flags().StringVarP(&serveNamespace, "namespace", "n", "", "Namespace to expose (default: auto-select)")
	serveCmd.Flags().StringVarP(&serveLogLevel, "log-level", "l", "info", "Log level (debug, info, warn, error)")
	serveCmd.Flags().BoolVar(&serveEager, "eager", false, "Pre-start all servers on init (default: lazy start)")
	serveCmd.Flags().BoolVar(&serveExposeManagerTools, "expose-manager-tools", false, "Include mcpmu.* tools in tools/list (default: hidden)")
	serveCmd.Flags().BoolVar(&serveResources, "resources", true, "Passthrough resources/* from upstream servers")
	serveCmd.Flags().BoolVar(&servePrompts, "prompts", true, "Passthrough prompts/* from upstream servers")
	serveCmd.Flags().BoolVar(&serveIsolated, "isolated", false, "Run embedded with private upstream server instances")

	serveCmd.Flags().BoolVar(&serveHTTP, "http", false, "Expose the endpoint over MCP Streamable HTTP instead of stdio")
	serveCmd.Flags().StringVar(&serveAddr, "addr", httpserve.DefaultAddr, "Listen address for --http")
	serveCmd.Flags().StringVar(&serveToken, "token", "", "Bearer token for --http (or set MCPMU_SERVE_TOKEN); required off-loopback")
	serveCmd.Flags().StringArrayVar(&serveAllowOrigins, "allow-origin", nil, "Extra allowed Origin for --http (repeatable; loopback origins are always allowed)")
	serveCmd.Flags().DurationVar(&serveSessionIdleTimeout, "session-idle-timeout", httpserve.DefaultSessionIdleTimeout, "Reap --http sessions idle for this long (0 = never)")

	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	serveLogLevel = strings.ToLower(serveLogLevel)
	// In stdio mode everything but the MCP protocol goes to stderr, filtered
	// to the requested level.
	if err := configureLogging(serveLogLevel, os.Stderr); err != nil {
		return err
	}
	if err := validateHTTPServeFlags(cmd); err != nil {
		return err
	}

	log.Printf("mcpmu serve starting (version=%s)", version)

	// Resolve and canonicalize the config path once for embedded watching and
	// daemon rendezvous identity.
	resolvedConfigPath, err := daemonConfigPath(false)
	if err != nil {
		return err
	}

	// Load configuration
	cfg, err := config.LoadFrom(resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	log.Printf("Loaded config with %d servers, %d namespaces", len(cfg.Servers), len(cfg.Namespaces))

	// A config written before the name was reserved, or edited by hand, can
	// still contain a server whose tools the aggregator can never route. Loading
	// does not fail on it — that would take every other server down too — so say
	// so on stderr, where it reaches the user rather than only the debug log.
	for _, name := range cfg.ReservedNameConflicts() {
		_, _ = fmt.Fprintf(os.Stderr,
			"mcpmu: server %q uses a reserved name; its tools cannot be called or filtered. "+
				"Rename it with: mcpmu rename %s <new-name>\n", name, name)
	}

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

	// HTTP serve is a deliberate foreground process that owns its own Core —
	// it never rendezvouses with the daemon (a network port opened as a side
	// effect of a client spawn would be a security surprise, and the daemon's
	// linger-driven lifetime can't outlive its sessions to accept the next
	// connection).
	if serveHTTP {
		return runHTTPServe(ctx, cfg, resolvedConfigPath)
	}

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

// validateHTTPServeFlags rejects flag combinations that make no sense with
// --http, and stray HTTP flags without it.
func validateHTTPServeFlags(cmd *cobra.Command) error {
	if serveHTTP {
		if serveIsolated {
			// --isolated's only effect is skipping the daemon rendezvous, and
			// HTTP serve never rendezvouses. Per-session privacy is the
			// per-server `shared: false` config property instead.
			return fmt.Errorf("--isolated cannot be combined with --http; use \"shared\": false on individual servers for per-session instances")
		}
		if serveToken == "" {
			serveToken = os.Getenv("MCPMU_SERVE_TOKEN")
		}
		return nil
	}
	for _, flag := range []string{"addr", "token", "allow-origin", "session-idle-timeout"} {
		if cmd.Flags().Changed(flag) {
			return fmt.Errorf("--%s requires --http", flag)
		}
	}
	return nil
}

// runHTTPServe owns one Core directly and serves it over Streamable HTTP
// until the context ends.
func runHTTPServe(ctx context.Context, cfg *config.Config, resolvedConfigPath string) error {
	core, err := server.NewCore(server.Options{
		Config:     cfg,
		ConfigPath: resolvedConfigPath, // For hot-reload watching
	})
	if err != nil {
		return fmt.Errorf("failed to create server core: %w", err)
	}
	defer core.Close()
	core.StartWatching(ctx)

	httpSrv, err := httpserve.New(httpserve.Options{
		Core:               core,
		Addr:               serveAddr,
		Token:              serveToken,
		AllowedOrigins:     serveAllowOrigins,
		SessionIdleTimeout: serveSessionIdleTimeout,
		Namespace:          serveNamespace,
		EagerStart:         serveEager,
		ExposeManagerTools: serveExposeManagerTools,
		ExposeResources:    serveResources,
		ExposePrompts:      servePrompts,
		ServerVersion:      version,
	})
	if err != nil {
		return err
	}

	if err := serveUntilShutdown(ctx, httpSrv, httpShutdownGrace); err != nil {
		return err
	}
	log.Println("mcpmu serve exiting")
	return nil
}

// httpShutdownGrace bounds the drain of in-flight requests on shutdown.
const httpShutdownGrace = 5 * time.Second

// httpListener is the slice of *httpserve.Server that serveUntilShutdown
// drives. An interface so the ordering below is testable without a socket.
type httpListener interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

// serveUntilShutdown serves until ctx ends, then shuts down — and, the point
// of the helper, does not return until that shutdown has finished.
// ListenAndServe returns the moment Shutdown closes the listener, well before
// the drain and session teardown complete; returning then would run the
// caller's deferred core.Close() — StopAll plus the final metrics flush —
// underneath tool calls that are still running.
func serveUntilShutdown(ctx context.Context, srv httpListener, grace time.Duration) error {
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP serve shutdown: %v", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil {
		// A listener error (port in use, bind refused) leaves the shutdown
		// goroutine parked on ctx.Done with nothing to drain; the process is
		// on its way out, so do not wait for it.
		return fmt.Errorf("http serve error: %w", err)
	}
	<-shutdownDone
	return nil
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
