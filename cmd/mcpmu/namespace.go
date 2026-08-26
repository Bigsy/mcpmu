package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/spf13/cobra"
)

var namespaceCmd = &cobra.Command{
	Use:     "namespace",
	Aliases: []string{"ns"},
	Short:   "Manage namespaces",
	Long: `Manage namespaces for grouping servers and tool permissions.

Namespaces allow you to define different contexts for accessing MCP servers.
Each namespace has its own set of assigned servers and tool permission rules.

Examples:
  mcpmu namespace add development --description "Dev environment"
  mcpmu namespace list
  mcpmu namespace assign development my-server
  mcpmu namespace default development`,
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
	namespaceCmd.AddCommand(namespaceSetCompressionCmd)
}

// ============================================================================
// namespace add
// ============================================================================

var (
	namespaceAddDescription string
)

var namespaceAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create a new namespace",
	Long: `Create a new namespace with the given name.

The namespace name must be unique.

Examples:
  mcpmu namespace add development
  mcpmu namespace add production --description "Production servers"`,
	Args: cobra.ExactArgs(1),
	RunE: runNamespaceAdd,
}

func init() {
	namespaceAddCmd.Flags().StringVarP(&namespaceAddDescription, "description", "d", "", "Description for the namespace")
}

func runNamespaceAdd(cmd *cobra.Command, args []string) error {
	name := args[0]

	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		ns := config.NamespaceConfig{
			Description: namespaceAddDescription,
		}

		if err := cfg.AddNamespace(name, ns); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}

	fmt.Printf("Added namespace %q\n", name)
	return nil
}

// ============================================================================
// namespace list
// ============================================================================

var (
	namespaceListJSON bool
)

var namespaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all namespaces",
	Long: `List all configured namespaces.

By default, outputs a human-readable table. Use --json for machine-readable output.

Examples:
  mcpmu namespace list
  mcpmu namespace list --json`,
	RunE: runNamespaceList,
}

func init() {
	namespaceListCmd.Flags().BoolVar(&namespaceListJSON, "json", false, "Output as JSON")
}

func runNamespaceList(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	namespaces := cfg.NamespaceEntries()
	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].Name < namespaces[j].Name
	})

	if namespaceListJSON {
		return outputNamespacesJSON(cfg, namespaces)
	}
	return outputNamespacesTable(cfg, namespaces)
}

func outputNamespacesJSON(cfg *config.Config, namespaces []config.NamespaceEntry) error {
	type namespaceView struct {
		Name          string   `json:"name"`
		Description   string   `json:"description,omitempty"`
		ServerCount   int      `json:"serverCount"`
		Servers       []string `json:"servers"`
		DenyByDefault bool     `json:"denyByDefault"`
		Compression   string   `json:"compression,omitempty"`
		IsDefault     bool     `json:"isDefault"`
	}

	views := make([]namespaceView, len(namespaces))
	for i, entry := range namespaces {
		views[i] = namespaceView{
			Name:          entry.Name,
			Description:   entry.Config.Description,
			ServerCount:   len(entry.Config.ServerIDs),
			Servers:       entry.Config.ServerIDs, // Server names are stored directly
			DenyByDefault: entry.Config.DenyByDefault,
			Compression:   entry.Config.Compression,
			IsDefault:     entry.Name == cfg.DefaultNamespace,
		}
	}

	data, err := json.MarshalIndent(views, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func outputNamespacesTable(cfg *config.Config, namespaces []config.NamespaceEntry) error {
	if len(namespaces) == 0 {
		fmt.Println("No namespaces configured")
		return nil
	}

	// Calculate column widths
	nameWidth := 4
	descWidth := 11

	for _, entry := range namespaces {
		if len(entry.Name) > nameWidth {
			nameWidth = len(entry.Name)
		}
		if len(entry.Config.Description) > descWidth {
			descWidth = len(entry.Config.Description)
		}
	}

	// Cap description width
	if descWidth > 30 {
		descWidth = 30
	}

	// Print header
	fmt.Printf("%-*s  %-*s  %s  %s  %s  %s\n", nameWidth, "NAME", descWidth, "DESCRIPTION", "SERVERS", "DENY-DEFAULT", "COMPRESS", "DEFAULT")

	// Print namespaces
	for _, entry := range namespaces {
		desc := entry.Config.Description
		if len(desc) > descWidth {
			desc = desc[:descWidth-3] + "..."
		}

		denyDefault := "no"
		if entry.Config.DenyByDefault {
			denyDefault = "yes"
		}

		compression := entry.Config.Compression
		if compression == "" {
			compression = "off"
		}

		isDefault := ""
		if entry.Name == cfg.DefaultNamespace {
			isDefault = "*"
		}

		fmt.Printf("%-*s  %-*s  %-7d  %-12s  %-8s  %s\n", nameWidth, entry.Name, descWidth, desc, len(entry.Config.ServerIDs), denyDefault, compression, isDefault)
	}

	return nil
}

// ============================================================================
// namespace remove
// ============================================================================

var (
	namespaceRemoveYes bool
)

var namespaceRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a namespace",
	Long: `Remove a namespace by name.

This will also remove all tool permissions associated with the namespace.

Examples:
  mcpmu namespace remove development
  mcpmu namespace remove development --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runNamespaceRemove,
}

func init() {
	namespaceRemoveCmd.Flags().BoolVarP(&namespaceRemoveYes, "yes", "y", false, "Skip confirmation prompt")
}

func runNamespaceRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	// Check namespace exists
	if err := requireNamespace(cfg, name); err != nil {
		return err
	}

	// Confirm unless --yes
	if !namespaceRemoveYes {
		confirmed, err := confirmAction(fmt.Sprintf("Remove namespace %q?", name))
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Cancelled")
			return nil
		}
	}

	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		return cfg.DeleteNamespace(name)
	}); err != nil {
		return err
	}

	fmt.Printf("Removed namespace %q\n", name)
	return nil
}

// ============================================================================
// namespace assign
// ============================================================================

var namespaceAssignCmd = &cobra.Command{
	Use:   "assign <namespace> <server>",
	Short: "Assign a server to a namespace",
	Long: `Assign a server to a namespace by name.

Both the namespace and server are specified by their names.

Examples:
  mcpmu namespace assign development my-server
  mcpmu namespace assign production api-server`,
	Args: cobra.ExactArgs(2),
	RunE: runNamespaceAssign,
}

func init() {
}

func runNamespaceAssign(cmd *cobra.Command, args []string) error {
	namespaceName := args[0]
	serverName := args[1]

	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		// Lookup namespace by name
		if err := requireNamespace(cfg, namespaceName); err != nil {
			return err
		}

		// Lookup server by name
		if err := requireServer(cfg, serverName); err != nil {
			return err
		}

		if err := cfg.AssignServerToNamespace(namespaceName, serverName); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}

	fmt.Printf("Assigned server %q to namespace %q\n", serverName, namespaceName)
	return nil
}

// ============================================================================
// namespace unassign
// ============================================================================

var namespaceUnassignCmd = &cobra.Command{
	Use:   "unassign <namespace> <server>",
	Short: "Unassign a server from a namespace",
	Long: `Unassign a server from a namespace by name.

Both the namespace and server are specified by their names.

Examples:
  mcpmu namespace unassign development my-server`,
	Args: cobra.ExactArgs(2),
	RunE: runNamespaceUnassign,
}

func init() {
}

func runNamespaceUnassign(cmd *cobra.Command, args []string) error {
	namespaceName := args[0]
	serverName := args[1]

	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		// Lookup namespace by name
		if err := requireNamespace(cfg, namespaceName); err != nil {
			return err
		}

		// Lookup server by name (optional - might want to unassign even if server was removed)
		if err := requireServer(cfg, serverName); err != nil {
			return err
		}

		if err := cfg.UnassignServerFromNamespace(namespaceName, serverName); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}

	fmt.Printf("Unassigned server %q from namespace %q\n", serverName, namespaceName)
	return nil
}

// ============================================================================
// namespace default
// ============================================================================

var namespaceDefaultCmd = &cobra.Command{
	Use:   "default <name>",
	Short: "Set the default namespace",
	Long: `Set the default namespace for stdio mode.

When no --namespace flag is provided to 'serve --stdio', this namespace is used.

Examples:
  mcpmu namespace default development`,
	Args: cobra.ExactArgs(1),
	RunE: runNamespaceDefault,
}

func init() {
}

func runNamespaceDefault(cmd *cobra.Command, args []string) error {
	name := args[0]

	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		// Lookup namespace by name
		if err := requireNamespace(cfg, name); err != nil {
			return err
		}

		cfg.DefaultNamespace = name

		return nil
	}); err != nil {
		return err
	}

	fmt.Printf("Set default namespace to %q\n", name)
	return nil
}

// ============================================================================
// namespace set-deny-default
// ============================================================================

var namespaceSetDenyDefaultCmd = &cobra.Command{
	Use:   "set-deny-default <namespace> <true|false>",
	Short: "Set whether unconfigured tools are denied",
	Long: `Set the deny-by-default setting for a namespace.

When set to true, tools without explicit permission are denied.
When set to false (default), tools without explicit permission are allowed.

Examples:
  mcpmu namespace set-deny-default production true
  mcpmu namespace set-deny-default development false`,
	Args: cobra.ExactArgs(2),
	RunE: runNamespaceSetDenyDefault,
}

func init() {
}

func runNamespaceSetDenyDefault(cmd *cobra.Command, args []string) error {
	namespaceName := args[0]
	// "true"/"deny" turn deny-by-default on.
	denyByDefault, err := parseBool(args[1])
	if err != nil {
		return err
	}

	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		// Lookup namespace by name
		ns, ok := cfg.GetNamespace(namespaceName)
		if !ok {
			return fmt.Errorf("namespace %q not found", namespaceName)
		}

		ns.DenyByDefault = denyByDefault

		if err := cfg.UpdateNamespace(namespaceName, ns); err != nil {
			return err
		}

		return nil
	}); err != nil {
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
// namespace set-compression
// ============================================================================

var namespaceSetCompressionCmd = &cobra.Command{
	Use:   "set-compression <namespace> <level|off>",
	Short: "Set the serve-mode compressed tool surface level",
	Long: `Set the compression level 'mcpmu serve' uses for this namespace.

When set, serve sessions on this namespace replace tools/list with the
list_tools/get_tool_schema/invoke_tool wrapper surface at the given level,
without needing the --compress flag. An explicit --compress flag overrides
this setting in both directions (--compress off forces it off).

Levels: low (full descriptions), medium (first sentence; recommended),
high (args only), max (names only). "off" clears the setting.

Examples:
  mcpmu namespace set-compression work medium
  mcpmu namespace set-compression work off`,
	Args: cobra.ExactArgs(2),
	RunE: runNamespaceSetCompression,
}

func runNamespaceSetCompression(cmd *cobra.Command, args []string) error {
	namespaceName := args[0]
	level := strings.ToLower(args[1])
	if level == "off" {
		level = ""
	}
	if err := config.ValidateCompression(level); err != nil {
		return err
	}

	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		ns, ok := cfg.GetNamespace(namespaceName)
		if !ok {
			return fmt.Errorf("namespace %q not found", namespaceName)
		}

		ns.Compression = level

		return cfg.UpdateNamespace(namespaceName, ns)
	}); err != nil {
		return err
	}

	if level == "" {
		fmt.Printf("Compression disabled for namespace %q\n", namespaceName)
	} else {
		fmt.Printf("Compression set to %q for namespace %q\n", level, namespaceName)
	}
	return nil
}
