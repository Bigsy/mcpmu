package main

import (
	"fmt"
	"strings"

	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/spf13/cobra"
)

var (
	addEnvFlags   []string
	addCwd        string
	addAutostart  bool
	addConfigPath string
	addURL        string
	addBearerEnv  string
	addScopes     []string
)

var addCmd = &cobra.Command{
	Use:   "add <name> [-- <command> [args...]]",
	Short: "Add a new MCP server",
	Long: `Add a new MCP server to the configuration.

For stdio servers, the server name must be unique. The command and arguments follow the -- separator.
For HTTP servers, use --url to specify the server endpoint.

Examples:
  # Stdio server
  mcp-studio add context7 -- npx -y @upstash/context7-mcp
  mcp-studio add my-server --env FOO=bar --env BAZ=qux -- ./server --flag
  mcp-studio add filesystem --cwd /home/user -- npx -y @anthropics/mcp-fs

  # HTTP server with bearer token
  mcp-studio add figma --url https://mcp.figma.com/mcp --bearer-env FIGMA_TOKEN

  # HTTP server with OAuth (login separately)
  mcp-studio add atlassian --url https://mcp.atlassian.com/mcp
  mcp-studio add atlassian --url https://mcp.atlassian.com/mcp --scopes read,write`,
	RunE: runAdd,
}

func init() {
	addCmd.Flags().StringArrayVarP(&addEnvFlags, "env", "e", nil, "Environment variable (KEY=VALUE), can be repeated")
	addCmd.Flags().StringVar(&addCwd, "cwd", "", "Working directory for the server")
	addCmd.Flags().BoolVar(&addAutostart, "autostart", false, "Start server automatically on app launch")
	addCmd.Flags().StringVarP(&addConfigPath, "config", "c", "", "Path to config file (default: ~/.config/mcp-studio/config.json)")
	addCmd.Flags().StringVar(&addURL, "url", "", "Server URL for HTTP transport (streamable HTTP)")
	addCmd.Flags().StringVar(&addBearerEnv, "bearer-env", "", "Environment variable containing bearer token")
	addCmd.Flags().StringSliceVar(&addScopes, "scopes", nil, "OAuth scopes to request (comma-separated)")

	rootCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	// Check if this is an HTTP server (--url provided)
	if addURL != "" {
		return runAddHTTP(cmd, args)
	}
	return runAddStdio(cmd, args)
}

func runAddStdio(cmd *cobra.Command, args []string) error {
	// Find the -- separator
	dashIdx := cmd.ArgsLenAtDash()
	if dashIdx == -1 {
		return fmt.Errorf("missing -- separator\n\nUsage: mcp-studio add <name> -- <command> [args...]")
	}

	// Args before -- are positional args (name)
	if dashIdx < 1 {
		return fmt.Errorf("missing server name\n\nUsage: mcp-studio add <name> -- <command> [args...]")
	}
	name := args[0]

	// Args after -- are the command
	cmdArgs := args[dashIdx:]
	if len(cmdArgs) < 1 {
		return fmt.Errorf("missing command after --\n\nUsage: mcp-studio add <name> -- <command> [args...]")
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
		Name:      name,
		Kind:      config.ServerKindStdio,
		Command:   cmdArgs[0],
		Args:      cmdArgs[1:],
		Cwd:       addCwd,
		Env:       env,
		Autostart: addAutostart,
	}

	// Add server (this enforces name uniqueness)
	if _, err := cfg.AddServer(srv); err != nil {
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
	// Need at least the name
	if len(args) < 1 {
		return fmt.Errorf("missing server name\n\nUsage: mcp-studio add <name> --url <url>")
	}
	name := args[0]

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
		Name:              name,
		Kind:              config.ServerKindStreamableHTTP,
		URL:               addURL,
		BearerTokenEnvVar: addBearerEnv,
		Scopes:            addScopes,
		Env:               env,
		Autostart:         addAutostart,
	}

	// Add server (this enforces name uniqueness)
	if _, err := cfg.AddServer(srv); err != nil {
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
