package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Server-level operations",
	Long:  `Server-level operations such as managing the global tool deny list.`,
}

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.AddCommand(serverDenyToolCmd)
	serverCmd.AddCommand(serverAllowToolCmd)
	serverCmd.AddCommand(serverDeniedToolsCmd)
}

// ============================================================================
// server deny-tool
// ============================================================================

var serverDenyToolCmd = &cobra.Command{
	Use:   "deny-tool <server> <tool> [<tool>...]",
	Short: "Add tools to a server's global deny list",
	Long: `Add one or more tools to a server's global deny list.

Globally denied tools are blocked regardless of namespace permissions.
This provides defense-in-depth: even if a namespace is misconfigured,
globally denied tools can never be called.

Adding a tool that is already denied is a no-op (idempotent).

Examples:
  mcpmu server deny-tool filesystem delete_file
  mcpmu server deny-tool filesystem delete_file move_file`,
	Args: cobra.MinimumNArgs(2),
	RunE: runServerDenyTool,
}

func runServerDenyTool(cmd *cobra.Command, args []string) error {
	serverName := args[0]
	toolNames := args[1:]

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	if _, ok := cfg.GetServer(serverName); !ok {
		return fmt.Errorf("server %q not found", serverName)
	}

	for _, toolName := range toolNames {
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			continue
		}
		toolName = normalizeToolName(toolName, serverName)
		if err := cfg.DenyTool(serverName, toolName); err != nil {
			return err
		}
	}

	if err := saveConfig(cfg, configPath); err != nil {
		return err
	}

	if len(toolNames) == 1 {
		fmt.Printf("Denied tool %q on server %q\n", normalizeToolName(toolNames[0], serverName), serverName)
	} else {
		fmt.Printf("Denied %d tools on server %q\n", len(toolNames), serverName)
	}
	return nil
}

// ============================================================================
// server allow-tool
// ============================================================================

var serverAllowToolCmd = &cobra.Command{
	Use:   "allow-tool <server> <tool> [<tool>...]",
	Short: "Remove tools from a server's global deny list",
	Long: `Remove one or more tools from a server's global deny list.

Removing a tool that is not denied is a no-op.

Examples:
  mcpmu server allow-tool filesystem delete_file
  mcpmu server allow-tool filesystem delete_file move_file`,
	Args: cobra.MinimumNArgs(2),
	RunE: runServerAllowTool,
}

func runServerAllowTool(cmd *cobra.Command, args []string) error {
	serverName := args[0]
	toolNames := args[1:]

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	if _, ok := cfg.GetServer(serverName); !ok {
		return fmt.Errorf("server %q not found", serverName)
	}

	for _, toolName := range toolNames {
		toolName = strings.TrimSpace(toolName)
		if toolName == "" {
			continue
		}
		toolName = normalizeToolName(toolName, serverName)
		if err := cfg.AllowTool(serverName, toolName); err != nil {
			return err
		}
	}

	if err := saveConfig(cfg, configPath); err != nil {
		return err
	}

	if len(toolNames) == 1 {
		fmt.Printf("Allowed tool %q on server %q\n", normalizeToolName(toolNames[0], serverName), serverName)
	} else {
		fmt.Printf("Allowed %d tools on server %q\n", len(toolNames), serverName)
	}
	return nil
}

// ============================================================================
// server denied-tools
// ============================================================================

var serverDeniedToolsJSON bool

var serverDeniedToolsCmd = &cobra.Command{
	Use:   "denied-tools <server>",
	Short: "List globally denied tools for a server",
	Long: `List all tools in a server's global deny list.

Examples:
  mcpmu server denied-tools filesystem
  mcpmu server denied-tools filesystem --json`,
	Args: cobra.ExactArgs(1),
	RunE: runServerDeniedTools,
}

func init() {
	serverDeniedToolsCmd.Flags().BoolVar(&serverDeniedToolsJSON, "json", false, "Output as JSON")
}

func runServerDeniedTools(cmd *cobra.Command, args []string) error {
	serverName := args[0]

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	tools, err := cfg.GetDeniedTools(serverName)
	if err != nil {
		return err
	}

	if serverDeniedToolsJSON {
		if tools == nil {
			tools = []string{}
		}
		data, err := json.MarshalIndent(tools, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	if len(tools) == 0 {
		fmt.Printf("No denied tools for server %q\n", serverName)
		return nil
	}

	fmt.Printf("Denied tools for server %q:\n", serverName)
	for _, tool := range tools {
		fmt.Printf("  %s\n", tool)
	}
	return nil
}
