package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/spf13/cobra"
)

var namespaceCmd = &cobra.Command{
	Use:   "namespace",
	Short: "Manage namespaces",
	Long: `Manage namespaces for grouping servers and tool permissions.

Namespaces allow you to define different contexts for accessing MCP servers.
Each namespace has its own set of assigned servers and tool permission rules.

Examples:
  mcp-studio namespace add development --description "Dev environment"
  mcp-studio namespace list
  mcp-studio namespace assign development my-server
  mcp-studio namespace default development`,
}

func init() {
	rootCmd.AddCommand(namespaceCmd)

	// Add subcommands
	namespaceCmd.AddCommand(namespaceAddCmd)
	namespaceCmd.AddCommand(namespaceListCmd)
	namespaceCmd.AddCommand(namespaceRemoveCmd)
	namespaceCmd.AddCommand(namespaceAssignCmd)
	namespaceCmd.AddCommand(namespaceUnassignCmd)
	namespaceCmd.AddCommand(namespaceDefaultCmd)
	namespaceCmd.AddCommand(namespaceSetDenyDefaultCmd)
}

// ============================================================================
// namespace add
// ============================================================================

var (
	namespaceAddDescription string
	namespaceAddConfigPath  string
)

var namespaceAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create a new namespace",
	Long: `Create a new namespace with the given name.

The namespace name must be unique.

Examples:
  mcp-studio namespace add development
  mcp-studio namespace add production --description "Production servers"`,
	Args: cobra.ExactArgs(1),
	RunE: runNamespaceAdd,
}

func init() {
	namespaceAddCmd.Flags().StringVarP(&namespaceAddDescription, "description", "d", "", "Description for the namespace")
	namespaceAddCmd.Flags().StringVarP(&namespaceAddConfigPath, "config", "c", "", "Path to config file")
}

func runNamespaceAdd(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := loadConfig(namespaceAddConfigPath)
	if err != nil {
		return err
	}

	ns := config.NamespaceConfig{
		Name:        name,
		Description: namespaceAddDescription,
	}

	if _, err := cfg.AddNamespace(ns); err != nil {
		return err
	}

	if err := saveConfig(cfg, namespaceAddConfigPath); err != nil {
		return err
	}

	fmt.Printf("Added namespace %q\n", name)
	return nil
}

// ============================================================================
// namespace list
// ============================================================================

var (
	namespaceListJSON       bool
	namespaceListConfigPath string
)

var namespaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all namespaces",
	Long: `List all configured namespaces.

By default, outputs a human-readable table. Use --json for machine-readable output.

Examples:
  mcp-studio namespace list
  mcp-studio namespace list --json`,
	RunE: runNamespaceList,
}

func init() {
	namespaceListCmd.Flags().BoolVar(&namespaceListJSON, "json", false, "Output as JSON")
	namespaceListCmd.Flags().StringVarP(&namespaceListConfigPath, "config", "c", "", "Path to config file")
}

func runNamespaceList(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig(namespaceListConfigPath)
	if err != nil {
		return err
	}

	namespaces := cfg.Namespaces
	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].Name < namespaces[j].Name
	})

	if namespaceListJSON {
		return outputNamespacesJSON(cfg, namespaces)
	}
	return outputNamespacesTable(cfg, namespaces)
}

func outputNamespacesJSON(cfg *config.Config, namespaces []config.NamespaceConfig) error {
	type namespaceView struct {
		Name          string   `json:"name"`
		Description   string   `json:"description,omitempty"`
		ServerCount   int      `json:"serverCount"`
		Servers       []string `json:"servers"`
		DenyByDefault bool     `json:"denyByDefault"`
		IsDefault     bool     `json:"isDefault"`
	}

	views := make([]namespaceView, len(namespaces))
	for i, ns := range namespaces {
		serverNames := resolveServerNames(cfg, ns.ServerIDs)
		views[i] = namespaceView{
			Name:          ns.Name,
			Description:   ns.Description,
			ServerCount:   len(ns.ServerIDs),
			Servers:       serverNames,
			DenyByDefault: ns.DenyByDefault,
			IsDefault:     ns.ID == cfg.DefaultNamespaceID,
		}
	}

	data, err := json.MarshalIndent(views, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func outputNamespacesTable(cfg *config.Config, namespaces []config.NamespaceConfig) error {
	if len(namespaces) == 0 {
		fmt.Println("No namespaces configured")
		return nil
	}

	// Calculate column widths
	nameWidth := 4
	descWidth := 11

	for _, ns := range namespaces {
		if len(ns.Name) > nameWidth {
			nameWidth = len(ns.Name)
		}
		if len(ns.Description) > descWidth {
			descWidth = len(ns.Description)
		}
	}

	// Cap description width
	if descWidth > 30 {
		descWidth = 30
	}

	// Print header
	fmt.Printf("%-*s  %-*s  %s  %s  %s\n", nameWidth, "NAME", descWidth, "DESCRIPTION", "SERVERS", "DENY-DEFAULT", "DEFAULT")

	// Print namespaces
	for _, ns := range namespaces {
		desc := ns.Description
		if len(desc) > descWidth {
			desc = desc[:descWidth-3] + "..."
		}

		denyDefault := "no"
		if ns.DenyByDefault {
			denyDefault = "yes"
		}

		isDefault := ""
		if ns.ID == cfg.DefaultNamespaceID {
			isDefault = "*"
		}

		fmt.Printf("%-*s  %-*s  %-7d  %-12s  %s\n", nameWidth, ns.Name, descWidth, desc, len(ns.ServerIDs), denyDefault, isDefault)
	}

	return nil
}

func resolveServerNames(cfg *config.Config, serverIDs []string) []string {
	names := make([]string, 0, len(serverIDs))
	for _, id := range serverIDs {
		srv := cfg.GetServer(id)
		if srv != nil {
			names = append(names, srv.Name)
		}
	}
	return names
}

// ============================================================================
// namespace remove
// ============================================================================

var (
	namespaceRemoveYes        bool
	namespaceRemoveConfigPath string
)

var namespaceRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a namespace",
	Long: `Remove a namespace by name.

This will also remove all tool permissions associated with the namespace.

Examples:
  mcp-studio namespace remove development
  mcp-studio namespace remove development --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runNamespaceRemove,
}

func init() {
	namespaceRemoveCmd.Flags().BoolVarP(&namespaceRemoveYes, "yes", "y", false, "Skip confirmation prompt")
	namespaceRemoveCmd.Flags().StringVarP(&namespaceRemoveConfigPath, "config", "c", "", "Path to config file")
}

func runNamespaceRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := loadConfig(namespaceRemoveConfigPath)
	if err != nil {
		return err
	}

	// Check namespace exists
	ns := cfg.FindNamespaceByName(name)
	if ns == nil {
		return fmt.Errorf("namespace %q not found", name)
	}

	// Confirm unless --yes
	if !namespaceRemoveYes {
		fmt.Printf("Remove namespace %q? [y/N] ", name)
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Println("Cancelled")
			return nil
		}
	}

	if err := cfg.DeleteNamespaceByName(name); err != nil {
		return err
	}

	if err := saveConfig(cfg, namespaceRemoveConfigPath); err != nil {
		return err
	}

	fmt.Printf("Removed namespace %q\n", name)
	return nil
}

// ============================================================================
// namespace assign
// ============================================================================

var namespaceAssignConfigPath string

var namespaceAssignCmd = &cobra.Command{
	Use:   "assign <namespace> <server>",
	Short: "Assign a server to a namespace",
	Long: `Assign a server to a namespace by name.

Both the namespace and server are specified by their names.

Examples:
  mcp-studio namespace assign development my-server
  mcp-studio namespace assign production api-server`,
	Args: cobra.ExactArgs(2),
	RunE: runNamespaceAssign,
}

func init() {
	namespaceAssignCmd.Flags().StringVarP(&namespaceAssignConfigPath, "config", "c", "", "Path to config file")
}

func runNamespaceAssign(cmd *cobra.Command, args []string) error {
	namespaceName := args[0]
	serverName := args[1]

	cfg, err := loadConfig(namespaceAssignConfigPath)
	if err != nil {
		return err
	}

	// Lookup namespace by name
	ns := cfg.FindNamespaceByName(namespaceName)
	if ns == nil {
		return fmt.Errorf("namespace %q not found", namespaceName)
	}

	// Lookup server by name
	srv := cfg.FindServerByName(serverName)
	if srv == nil {
		return fmt.Errorf("server %q not found", serverName)
	}

	if err := cfg.AssignServerToNamespace(ns.ID, srv.ID); err != nil {
		return err
	}

	if err := saveConfig(cfg, namespaceAssignConfigPath); err != nil {
		return err
	}

	fmt.Printf("Assigned server %q to namespace %q\n", serverName, namespaceName)
	return nil
}

// ============================================================================
// namespace unassign
// ============================================================================

var namespaceUnassignConfigPath string

var namespaceUnassignCmd = &cobra.Command{
	Use:   "unassign <namespace> <server>",
	Short: "Unassign a server from a namespace",
	Long: `Unassign a server from a namespace by name.

Both the namespace and server are specified by their names.

Examples:
  mcp-studio namespace unassign development my-server`,
	Args: cobra.ExactArgs(2),
	RunE: runNamespaceUnassign,
}

func init() {
	namespaceUnassignCmd.Flags().StringVarP(&namespaceUnassignConfigPath, "config", "c", "", "Path to config file")
}

func runNamespaceUnassign(cmd *cobra.Command, args []string) error {
	namespaceName := args[0]
	serverName := args[1]

	cfg, err := loadConfig(namespaceUnassignConfigPath)
	if err != nil {
		return err
	}

	// Lookup namespace by name
	ns := cfg.FindNamespaceByName(namespaceName)
	if ns == nil {
		return fmt.Errorf("namespace %q not found", namespaceName)
	}

	// Lookup server by name
	srv := cfg.FindServerByName(serverName)
	if srv == nil {
		return fmt.Errorf("server %q not found", serverName)
	}

	if err := cfg.UnassignServerFromNamespace(ns.ID, srv.ID); err != nil {
		return err
	}

	if err := saveConfig(cfg, namespaceUnassignConfigPath); err != nil {
		return err
	}

	fmt.Printf("Unassigned server %q from namespace %q\n", serverName, namespaceName)
	return nil
}

// ============================================================================
// namespace default
// ============================================================================

var namespaceDefaultConfigPath string

var namespaceDefaultCmd = &cobra.Command{
	Use:   "default <name>",
	Short: "Set the default namespace",
	Long: `Set the default namespace for stdio mode.

When no --namespace flag is provided to 'serve --stdio', this namespace is used.

Examples:
  mcp-studio namespace default development`,
	Args: cobra.ExactArgs(1),
	RunE: runNamespaceDefault,
}

func init() {
	namespaceDefaultCmd.Flags().StringVarP(&namespaceDefaultConfigPath, "config", "c", "", "Path to config file")
}

func runNamespaceDefault(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := loadConfig(namespaceDefaultConfigPath)
	if err != nil {
		return err
	}

	// Lookup namespace by name
	ns := cfg.FindNamespaceByName(name)
	if ns == nil {
		return fmt.Errorf("namespace %q not found", name)
	}

	cfg.DefaultNamespaceID = ns.ID

	if err := saveConfig(cfg, namespaceDefaultConfigPath); err != nil {
		return err
	}

	fmt.Printf("Set default namespace to %q\n", name)
	return nil
}

// ============================================================================
// namespace set-deny-default
// ============================================================================

var namespaceSetDenyDefaultConfigPath string

var namespaceSetDenyDefaultCmd = &cobra.Command{
	Use:   "set-deny-default <namespace> <true|false>",
	Short: "Set whether unconfigured tools are denied",
	Long: `Set the deny-by-default setting for a namespace.

When set to true, tools without explicit permission are denied.
When set to false (default), tools without explicit permission are allowed.

Examples:
  mcp-studio namespace set-deny-default production true
  mcp-studio namespace set-deny-default development false`,
	Args: cobra.ExactArgs(2),
	RunE: runNamespaceSetDenyDefault,
}

func init() {
	namespaceSetDenyDefaultCmd.Flags().StringVarP(&namespaceSetDenyDefaultConfigPath, "config", "c", "", "Path to config file")
}

func runNamespaceSetDenyDefault(cmd *cobra.Command, args []string) error {
	namespaceName := args[0]
	valueStr := strings.ToLower(args[1])

	var denyByDefault bool
	switch valueStr {
	case "true", "yes", "1":
		denyByDefault = true
	case "false", "no", "0":
		denyByDefault = false
	default:
		return fmt.Errorf("invalid value %q: expected true or false", args[1])
	}

	cfg, err := loadConfig(namespaceSetDenyDefaultConfigPath)
	if err != nil {
		return err
	}

	// Lookup namespace by name
	ns := cfg.FindNamespaceByName(namespaceName)
	if ns == nil {
		return fmt.Errorf("namespace %q not found", namespaceName)
	}

	ns.DenyByDefault = denyByDefault

	if err := cfg.UpdateNamespace(*ns); err != nil {
		return err
	}

	if err := saveConfig(cfg, namespaceSetDenyDefaultConfigPath); err != nil {
		return err
	}

	setting := "disabled"
	if denyByDefault {
		setting = "enabled"
	}
	fmt.Printf("Deny-by-default %s for namespace %q\n", setting, namespaceName)
	return nil
}

// ============================================================================
// helpers
// ============================================================================

func loadConfig(configPath string) (*config.Config, error) {
	var cfg *config.Config
	var err error
	if configPath != "" {
		cfg, err = config.LoadFrom(configPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return cfg, nil
}

func saveConfig(cfg *config.Config, configPath string) error {
	if configPath != "" {
		if err := config.SaveTo(cfg, configPath); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
	} else {
		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
	}
	return nil
}
