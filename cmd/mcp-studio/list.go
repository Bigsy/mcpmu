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
		URL       string            `json:"url,omitempty"`
		Cwd       string            `json:"cwd,omitempty"`
		Env       map[string]string `json:"env,omitempty"`
		Enabled   bool              `json:"enabled"`
		Autostart bool              `json:"autostart"`
		Auth      string            `json:"auth,omitempty"`
	}

	views := make([]serverView, len(servers))
	for i, srv := range servers {
		views[i] = serverView{
			Name:      srv.Name,
			Kind:      string(srv.GetKind()),
			Command:   srv.Command,
			Args:      srv.Args,
			URL:       srv.URL,
			Cwd:       srv.Cwd,
			Env:       srv.Env,
			Enabled:   srv.IsEnabled(),
			Autostart: srv.Autostart,
			Auth:      getAuthType(srv),
		}
	}

	data, err := json.MarshalIndent(views, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func getAuthType(srv config.ServerConfig) string {
	if !srv.IsHTTP() {
		return "-"
	}
	if srv.BearerTokenEnvVar != "" {
		return "bearer"
	}
	// For OAuth servers, we'd need to check the credential store
	// For now, just indicate OAuth is potentially configured
	return "oauth"
}

func outputTable(servers []config.ServerConfig) error {
	if len(servers) == 0 {
		fmt.Println("No servers configured")
		return nil
	}

	// Calculate column widths
	nameWidth := 4     // "NAME"
	typeWidth := 4     // "TYPE"
	cmdWidth := 15     // "COMMAND/URL"

	for _, srv := range servers {
		if len(srv.Name) > nameWidth {
			nameWidth = len(srv.Name)
		}
		kindStr := formatKind(srv)
		if len(kindStr) > typeWidth {
			typeWidth = len(kindStr)
		}
		cmdStr := formatCommandOrURL(srv)
		if len(cmdStr) > cmdWidth {
			cmdWidth = len(cmdStr)
		}
	}

	// Cap widths for readability
	if cmdWidth > 35 {
		cmdWidth = 35
	}

	// Print header
	fmt.Printf("%-*s  %-*s  %-*s  %-8s  %s\n", nameWidth, "NAME", typeWidth, "TYPE", cmdWidth, "COMMAND/URL", "AUTH", "ENABLED")

	// Print servers
	for _, srv := range servers {
		cmdStr := formatCommandOrURL(srv)
		if len(cmdStr) > cmdWidth {
			cmdStr = cmdStr[:cmdWidth-3] + "..."
		}

		enabled := "yes"
		if !srv.IsEnabled() {
			enabled = "no"
		}

		auth := getAuthType(srv)
		kindStr := formatKind(srv)

		fmt.Printf("%-*s  %-*s  %-*s  %-8s  %s\n", nameWidth, srv.Name, typeWidth, kindStr, cmdWidth, cmdStr, auth, enabled)
	}

	return nil
}

func formatKind(srv config.ServerConfig) string {
	kind := srv.GetKind()
	switch kind {
	case config.ServerKindStdio:
		return "stdio"
	case config.ServerKindStreamableHTTP:
		return "http"
	default:
		return string(kind)
	}
}

func formatCommandOrURL(srv config.ServerConfig) string {
	if srv.IsHTTP() {
		return srv.URL
	}
	return formatCommand(srv)
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
