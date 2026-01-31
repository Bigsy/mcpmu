package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/oauth"
	"github.com/spf13/cobra"
)

var (
	mcpConfigPath string
	mcpScopes     []string
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP server management commands",
	Long:  `Commands for managing MCP server authentication and status.`,
}

var mcpLoginCmd = &cobra.Command{
	Use:   "login <server-name>",
	Short: "Login to an OAuth-enabled MCP server",
	Long: `Initiate OAuth login for a remote MCP server.

This will:
1. Discover the server's OAuth configuration
2. Open your browser for authentication
3. Store the obtained credentials securely

Examples:
  mcpmu mcp login atlassian
  mcpmu mcp login figma --scopes read,write`,
	Args: cobra.ExactArgs(1),
	RunE: runMCPLogin,
}

var mcpLogoutCmd = &cobra.Command{
	Use:   "logout <server-name>",
	Short: "Logout from an MCP server",
	Long: `Remove stored OAuth credentials for an MCP server.

Examples:
  mcpmu mcp logout atlassian`,
	Args: cobra.ExactArgs(1),
	RunE: runMCPLogout,
}

func init() {
	mcpCmd.PersistentFlags().StringVarP(&mcpConfigPath, "config", "c", "", "Path to config file")

	mcpLoginCmd.Flags().StringSliceVar(&mcpScopes, "scopes", nil, "OAuth scopes to request (comma-separated)")

	mcpCmd.AddCommand(mcpLoginCmd)
	mcpCmd.AddCommand(mcpLogoutCmd)

	rootCmd.AddCommand(mcpCmd)
}

func runMCPLogin(cmd *cobra.Command, args []string) error {
	serverName := args[0]

	// Load config
	var cfg *config.Config
	var err error
	if mcpConfigPath != "" {
		cfg, err = config.LoadFrom(mcpConfigPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Find server
	srv := cfg.FindServerByName(serverName)
	if srv == nil {
		return fmt.Errorf("server %q not found", serverName)
	}

	// Verify it's an HTTP server
	if !srv.IsHTTP() {
		return fmt.Errorf("server %q is not an HTTP server (OAuth not applicable)", serverName)
	}

	// Check if bearer token is configured (no OAuth needed)
	if srv.BearerTokenEnvVar != "" {
		return fmt.Errorf("server %q uses bearer token auth (set via %s), not OAuth", serverName, srv.BearerTokenEnvVar)
	}

	// Create credential store
	storeMode := oauth.StoreMode(cfg.MCPOAuthCredentialStore)
	store, err := oauth.NewCredentialStore(storeMode)
	if err != nil {
		return fmt.Errorf("failed to create credential store: %w", err)
	}

	// Merge scopes from config and CLI
	scopes := srv.Scopes
	if len(mcpScopes) > 0 {
		scopes = mcpScopes
	}

	// Run OAuth flow
	flowConfig := oauth.FlowConfig{
		ServerURL:    srv.URL,
		ServerName:   srv.Name,
		Scopes:       scopes,
		CallbackPort: cfg.MCPOAuthCallbackPort,
		Store:        store,
	}

	fmt.Printf("Starting OAuth login for %s...\n", serverName)
	fmt.Println("Your browser will open for authentication.")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	flow := oauth.NewFlow(flowConfig)
	if err := flow.Run(ctx); err != nil {
		return fmt.Errorf("OAuth login failed: %w", err)
	}

	fmt.Printf("Successfully logged in to %s\n", serverName)
	return nil
}

func runMCPLogout(cmd *cobra.Command, args []string) error {
	serverName := args[0]

	// Load config
	var cfg *config.Config
	var err error
	if mcpConfigPath != "" {
		cfg, err = config.LoadFrom(mcpConfigPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Find server
	srv := cfg.FindServerByName(serverName)
	if srv == nil {
		return fmt.Errorf("server %q not found", serverName)
	}

	// Verify it's an HTTP server
	if !srv.IsHTTP() {
		return fmt.Errorf("server %q is not an HTTP server", serverName)
	}

	// Create credential store
	storeMode := oauth.StoreMode(cfg.MCPOAuthCredentialStore)
	store, err := oauth.NewCredentialStore(storeMode)
	if err != nil {
		return fmt.Errorf("failed to create credential store: %w", err)
	}

	// Delete credentials
	if err := store.Delete(srv.URL); err != nil {
		return fmt.Errorf("failed to remove credentials: %w", err)
	}

	fmt.Printf("Logged out from %s\n", serverName)
	return nil
}
