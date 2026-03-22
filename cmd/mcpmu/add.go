package main

import (
	"fmt"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/spf13/cobra"
)

var (
	addEnvFlags          []string
	addCwd               string
	addAutostart         bool
	addConfigPath        string
	addURL               string
	addBearerEnv         string
	addScopes            []string
	addOAuthClientID     string
	addOAuthCallbackPort int
	addStartupTimeout    int
	addToolTimeout       int
)

var addCmd = &cobra.Command{
	Use:   "add <name> [<url> | -- <command> [args...]]",
	Short: "Add a new MCP server",
	Long: `Add a new MCP server to the configuration.

For stdio servers, the command and arguments follow the -- separator.
For HTTP servers, provide the URL as a positional argument (or use --url).

Examples:
  # Stdio server
  mcpmu add context7 -- npx -y @upstash/context7-mcp
  mcpmu add my-server --env FOO=bar --env BAZ=qux -- ./server --flag
  mcpmu add filesystem --cwd /home/user -- npx -y @anthropics/mcp-fs

  # HTTP server with bearer token
  mcpmu add figma https://mcp.figma.com/mcp --bearer-env FIGMA_TOKEN

  # HTTP server with OAuth (login separately)
  mcpmu add atlassian https://mcp.atlassian.com/mcp --scopes read,write

  # HTTP server with pre-registered OAuth client
  mcpmu add slack https://mcp.slack.com/mcp --oauth-client-id 1601185624273.8899143856786 --oauth-callback-port 3118`,
	RunE: runAdd,
}

func init() {
	addCmd.Flags().StringArrayVarP(&addEnvFlags, "env", "e", nil, "Environment variable (KEY=VALUE), can be repeated")
	addCmd.Flags().StringVar(&addCwd, "cwd", "", "Working directory for the server")
	addCmd.Flags().BoolVar(&addAutostart, "autostart", false, "Start server automatically on app launch")
	addCmd.Flags().StringVarP(&addConfigPath, "config", "c", "", "Path to config file (default: ~/.config/mcpmu/config.json)")
	addCmd.Flags().StringVar(&addURL, "url", "", "Server URL for HTTP transport (streamable HTTP)")
	addCmd.Flags().StringVar(&addBearerEnv, "bearer-env", "", "Environment variable containing bearer token")
	addCmd.Flags().StringSliceVar(&addScopes, "scopes", nil, "OAuth scopes to request (comma-separated)")
	addCmd.Flags().StringVar(&addOAuthClientID, "oauth-client-id", "", "Pre-registered OAuth client ID")
	addCmd.Flags().IntVar(&addOAuthCallbackPort, "oauth-callback-port", 0, "OAuth callback port (1-65535)")
	addCmd.Flags().IntVar(&addStartupTimeout, "startup-timeout", 0, "Startup timeout in seconds (default: 10)")
	addCmd.Flags().IntVar(&addToolTimeout, "tool-timeout", 0, "Tool call timeout in seconds (default: 60)")

	rootCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	// Check if this is an HTTP server:
	// 1. --url flag provided, or
	// 2. Second positional arg looks like a URL
	if addURL != "" {
		return runAddHTTP(cmd, args)
	}

	// Check if second arg is a URL (no -- separator case)
	dashIdx := cmd.ArgsLenAtDash()
	if dashIdx == -1 && len(args) >= 2 && isURL(args[1]) {
		addURL = args[1]
		return runAddHTTP(cmd, args[:1]) // pass only the name
	}

	return runAddStdio(cmd, args)
}

// isURL checks if a string looks like an HTTP(S) URL.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func validateTimeoutFlags() error {
	if addStartupTimeout < 0 {
		return fmt.Errorf("--startup-timeout must be a positive number")
	}
	if addToolTimeout < 0 {
		return fmt.Errorf("--tool-timeout must be a positive number")
	}
	return nil
}

func runAddStdio(cmd *cobra.Command, args []string) error {
	if err := validateTimeoutFlags(); err != nil {
		return err
	}
	// Reject HTTP-only flags on stdio servers
	if addBearerEnv != "" {
		return fmt.Errorf("--bearer-env is only valid for HTTP servers")
	}
	if addOAuthClientID != "" || addOAuthCallbackPort > 0 || len(addScopes) > 0 {
		return fmt.Errorf("--oauth-client-id, --oauth-callback-port, and --scopes are only valid for HTTP servers")
	}

	// Find the -- separator
	dashIdx := cmd.ArgsLenAtDash()
	if dashIdx == -1 {
		return fmt.Errorf("missing -- separator\n\nUsage: mcpmu add <name> -- <command> [args...]")
	}

	// Args before -- are positional args (name)
	if dashIdx < 1 {
		return fmt.Errorf("missing server name\n\nUsage: mcpmu add <name> -- <command> [args...]")
	}
	name := args[0]

	// Args after -- are the command
	cmdArgs := args[dashIdx:]
	if len(cmdArgs) < 1 {
		return fmt.Errorf("missing command after --\n\nUsage: mcpmu add <name> -- <command> [args...]")
	}

	// Parse environment variables
	env, err := parseEnvFlags(addEnvFlags)
	if err != nil {
		return err
	}

	// Load config
	var cfg *config.Config
	if addConfigPath != "" {
		cfg, err = config.LoadFrom(addConfigPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Build server config
	srv := config.ServerConfig{
		Command:           cmdArgs[0],
		Args:              cmdArgs[1:],
		Cwd:               addCwd,
		Env:               env,
		Autostart:         addAutostart,
		StartupTimeoutSec: addStartupTimeout,
		ToolTimeoutSec:    addToolTimeout,
	}

	// Add server (this enforces name uniqueness)
	if err := cfg.AddServer(name, srv); err != nil {
		return err
	}

	// Save config
	if addConfigPath != "" {
		if err := config.SaveTo(cfg, addConfigPath); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
	} else {
		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
	}

	fmt.Printf("Added server %q\n", name)
	return nil
}

func runAddHTTP(cmd *cobra.Command, args []string) error {
	if err := validateTimeoutFlags(); err != nil {
		return err
	}
	// Need at least the name
	if len(args) < 1 {
		return fmt.Errorf("missing server name\n\nUsage: mcpmu add <name> <url>")
	}
	name := args[0]

	// Reject mutually exclusive auth modes
	hasOAuthFlags := addOAuthClientID != "" || len(addScopes) > 0 || addOAuthCallbackPort > 0
	if addBearerEnv != "" && hasOAuthFlags {
		return fmt.Errorf("--bearer-env and OAuth flags (--oauth-client-id, --scopes, --oauth-callback-port) are mutually exclusive")
	}

	// Parse environment variables
	env, err := parseEnvFlags(addEnvFlags)
	if err != nil {
		return err
	}

	// Load config
	var cfg *config.Config
	if addConfigPath != "" {
		cfg, err = config.LoadFrom(addConfigPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Build server config
	srv := config.ServerConfig{
		URL:               addURL,
		BearerTokenEnvVar: addBearerEnv,
		Env:               env,
		Autostart:         addAutostart,
		StartupTimeoutSec: addStartupTimeout,
		ToolTimeoutSec:    addToolTimeout,
	}

	// Build OAuth config if any OAuth-related flags are provided
	if len(addScopes) > 0 || addOAuthClientID != "" || addOAuthCallbackPort > 0 {
		srv.OAuth = &config.OAuthConfig{
			ClientID: addOAuthClientID,
			Scopes:   addScopes,
		}
		if addOAuthCallbackPort > 0 {
			port := addOAuthCallbackPort
			srv.OAuth.CallbackPort = &port
		}
	}

	// Add server (this enforces name uniqueness)
	if err := cfg.AddServer(name, srv); err != nil {
		return err
	}

	// Save config
	if addConfigPath != "" {
		if err := config.SaveTo(cfg, addConfigPath); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
	} else {
		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
	}

	fmt.Printf("Added HTTP server %q (%s)\n", name, addURL)
	return nil
}

// parseEnvFlags parses KEY=VALUE pairs from --env flags.
func parseEnvFlags(flags []string) (map[string]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}

	env := make(map[string]string)
	for _, kv := range flags {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid --env format %q: expected KEY=VALUE", kv)
		}
		key := parts[0]
		if key == "" {
			return nil, fmt.Errorf("invalid --env format %q: key cannot be empty", kv)
		}
		env[key] = parts[1]
	}
	return env, nil
}
