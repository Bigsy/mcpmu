package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/spf13/cobra"
)

var (
	listJSON       bool
	listConfigPath string
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured MCP servers",
	Long: `List all configured MCP servers.

By default, outputs a human-readable table. Use --json for machine-readable output.

Examples:
  mcp-studio list
  mcp-studio list --json`,
	RunE: runList,
}

func init() {
	listCmd.Flags().BoolVar(&listJSON, "json", false, "Output as JSON")
	listCmd.Flags().StringVarP(&listConfigPath, "config", "c", "", "Path to config file (default: ~/.config/mcp-studio/config.json)")

	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	// Load config
	var cfg *config.Config
	var err error
	if listConfigPath != "" {
		cfg, err = config.LoadFrom(listConfigPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Get servers sorted by name
	servers := cfg.ServerList()
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].Name < servers[j].Name
	})

	if listJSON {
		return outputJSON(servers)
	}
	return outputTable(servers)
}

func outputJSON(servers []config.ServerConfig) error {
	// Create a simplified view without internal IDs
	type serverView struct {
		Name      string            `json:"name"`
		Kind      string            `json:"kind"`
		Command   string            `json:"command,omitempty"`
		Args      []string          `json:"args,omitempty"`
		Cwd       string            `json:"cwd,omitempty"`
		Env       map[string]string `json:"env,omitempty"`
		Enabled   bool              `json:"enabled"`
		Autostart bool              `json:"autostart"`
	}

	views := make([]serverView, len(servers))
	for i, srv := range servers {
		views[i] = serverView{
			Name:      srv.Name,
			Kind:      string(srv.Kind),
			Command:   srv.Command,
			Args:      srv.Args,
			Cwd:       srv.Cwd,
			Env:       srv.Env,
			Enabled:   srv.IsEnabled(),
			Autostart: srv.Autostart,
		}
	}

	data, err := json.MarshalIndent(views, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func outputTable(servers []config.ServerConfig) error {
	if len(servers) == 0 {
		fmt.Println("No servers configured")
		return nil
	}

	// Calculate column widths
	nameWidth := 4 // "NAME"
	cmdWidth := 7  // "COMMAND"

	for _, srv := range servers {
		if len(srv.Name) > nameWidth {
			nameWidth = len(srv.Name)
		}
		cmdStr := formatCommand(srv)
		if len(cmdStr) > cmdWidth {
			cmdWidth = len(cmdStr)
		}
	}

	// Cap command width for readability
	if cmdWidth > 40 {
		cmdWidth = 40
	}

	// Print header
	fmt.Printf("%-*s  %-*s  %s\n", nameWidth, "NAME", cmdWidth, "COMMAND", "ENABLED")

	// Print servers
	for _, srv := range servers {
		cmdStr := formatCommand(srv)
		if len(cmdStr) > cmdWidth {
			cmdStr = cmdStr[:cmdWidth-3] + "..."
		}

		enabled := "yes"
		if !srv.IsEnabled() {
			enabled = "no"
		}

		fmt.Printf("%-*s  %-*s  %s\n", nameWidth, srv.Name, cmdWidth, cmdStr, enabled)
	}

	return nil
}

func formatCommand(srv config.ServerConfig) string {
	if srv.Command == "" {
		return ""
	}
	if len(srv.Args) == 0 {
		return srv.Command
	}
	return srv.Command + " " + strings.Join(srv.Args, " ")
}
