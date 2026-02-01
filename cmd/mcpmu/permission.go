package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/spf13/cobra"
)

var permissionCmd = &cobra.Command{
	Use:   "permission",
	Short: "Manage tool permissions",
	Long: `Manage tool permissions within namespaces.

Tool permissions control which tools can be called when using a namespace.
Permissions are specified per namespace, per server, per tool.

Examples:
  mcpmu permission set production api-server create_user deny
  mcpmu permission list production
  mcpmu permission unset production api-server create_user`,
}

func init() {
	rootCmd.AddCommand(permissionCmd)

	// Add subcommands
	permissionCmd.AddCommand(permissionSetCmd)
	permissionCmd.AddCommand(permissionUnsetCmd)
	permissionCmd.AddCommand(permissionListCmd)
}

// ============================================================================
// permission set
// ============================================================================

var permissionSetConfigPath string

var permissionSetCmd = &cobra.Command{
	Use:   "set <namespace> <server> <tool> <allow|deny>",
	Short: "Set a tool permission",
	Long: `Set an explicit permission for a tool within a namespace.

The namespace and server are specified by name.
The tool name should be unqualified (e.g., "read_file").
Qualified names like "<server-id>.read_file" are accepted for convenience.
Tool names that include dots are allowed (e.g., "fs.read_file").

Examples:
  mcpmu permission set production api-server create_user deny
  mcpmu permission set development filesystem read_file allow`,
	Args: cobra.ExactArgs(4),
	RunE: runPermissionSet,
}

func init() {
	permissionSetCmd.Flags().StringVarP(&permissionSetConfigPath, "config", "c", "", "Path to config file")
}

func runPermissionSet(cmd *cobra.Command, args []string) error {
	namespaceName := args[0]
	serverName := args[1]
	toolNameRaw := strings.TrimSpace(args[2])
	permStr := strings.ToLower(args[3])

	var enabled bool
	switch permStr {
	case "allow", "yes", "true", "1":
		enabled = true
	case "deny", "no", "false", "0":
		enabled = false
	default:
		return fmt.Errorf("invalid permission %q: expected allow or deny", args[3])
	}

	cfg, err := loadConfig(permissionSetConfigPath)
	if err != nil {
		return err
	}

	// Lookup namespace by name
	if _, ok := cfg.GetNamespace(namespaceName); !ok {
		return fmt.Errorf("namespace %q not found", namespaceName)
	}

	// Lookup server by name
	if _, ok := cfg.GetServer(serverName); !ok {
		return fmt.Errorf("server %q not found", serverName)
	}

	toolName := normalizeToolName(toolNameRaw, serverName)

	if err := cfg.SetToolPermission(namespaceName, serverName, toolName, enabled); err != nil {
		return err
	}

	if err := saveConfig(cfg, permissionSetConfigPath); err != nil {
		return err
	}

	permission := "allowed"
	if !enabled {
		permission = "denied"
	}
	fmt.Printf("Set %s.%s to %s in namespace %q\n", serverName, toolName, permission, namespaceName)
	return nil
}

// ============================================================================
// permission unset
// ============================================================================

var permissionUnsetConfigPath string

var permissionUnsetCmd = &cobra.Command{
	Use:   "unset <namespace> <server> <tool>",
	Short: "Remove a tool permission",
	Long: `Remove an explicit permission for a tool, reverting to namespace default.

The namespace and server are specified by name.
The tool name should be unqualified (e.g., "read_file").
Qualified names like "<server-id>.read_file" are accepted for convenience.
Tool names that include dots are allowed (e.g., "fs.read_file").

Examples:
  mcpmu permission unset production api-server create_user`,
	Args: cobra.ExactArgs(3),
	RunE: runPermissionUnset,
}

func init() {
	permissionUnsetCmd.Flags().StringVarP(&permissionUnsetConfigPath, "config", "c", "", "Path to config file")
}

func runPermissionUnset(cmd *cobra.Command, args []string) error {
	namespaceName := args[0]
	serverName := args[1]
	toolNameRaw := strings.TrimSpace(args[2])

	cfg, err := loadConfig(permissionUnsetConfigPath)
	if err != nil {
		return err
	}

	// Lookup namespace by name
	if _, ok := cfg.GetNamespace(namespaceName); !ok {
		return fmt.Errorf("namespace %q not found", namespaceName)
	}

	// Lookup server by name
	if _, ok := cfg.GetServer(serverName); !ok {
		return fmt.Errorf("server %q not found", serverName)
	}

	toolName := normalizeToolName(toolNameRaw, serverName)

	if err := cfg.UnsetToolPermission(namespaceName, serverName, toolName); err != nil {
		return err
	}

	if err := saveConfig(cfg, permissionUnsetConfigPath); err != nil {
		return err
	}

	fmt.Printf("Removed permission for %s.%s in namespace %q\n", serverName, toolName, namespaceName)
	return nil
}

// ============================================================================
// permission list
// ============================================================================

var (
	permissionListJSON       bool
	permissionListConfigPath string
)

var permissionListCmd = &cobra.Command{
	Use:   "list <namespace>",
	Short: "List tool permissions for a namespace",
	Long: `List all tool permissions for a namespace.

By default, outputs a human-readable table. Use --json for machine-readable output.

Examples:
  mcpmu permission list production
  mcpmu permission list production --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPermissionList,
}

func init() {
	permissionListCmd.Flags().BoolVar(&permissionListJSON, "json", false, "Output as JSON")
	permissionListCmd.Flags().StringVarP(&permissionListConfigPath, "config", "c", "", "Path to config file")
}

func runPermissionList(cmd *cobra.Command, args []string) error {
	namespaceName := args[0]

	cfg, err := loadConfig(permissionListConfigPath)
	if err != nil {
		return err
	}

	// Lookup namespace by name
	ns, ok := cfg.GetNamespace(namespaceName)
	if !ok {
		return fmt.Errorf("namespace %q not found", namespaceName)
	}

	permissions := cfg.GetToolPermissionsForNamespace(namespaceName)

	// Sort by server, then tool
	sort.Slice(permissions, func(i, j int) bool {
		if permissions[i].Server != permissions[j].Server {
			return permissions[i].Server < permissions[j].Server
		}
		return permissions[i].ToolName < permissions[j].ToolName
	})

	if permissionListJSON {
		return outputPermissionsJSON(namespaceName, &ns, permissions)
	}
	return outputPermissionsTable(namespaceName, &ns, permissions)
}

func outputPermissionsJSON(namespaceName string, ns *config.NamespaceConfig, permissions []config.ToolPermission) error {
	type permissionView struct {
		Server     string `json:"server"`
		Tool       string `json:"tool"`
		Permission string `json:"permission"`
	}

	type resultView struct {
		Namespace     string           `json:"namespace"`
		DenyByDefault bool             `json:"denyByDefault"`
		Permissions   []permissionView `json:"permissions"`
	}

	views := make([]permissionView, len(permissions))
	for i, p := range permissions {
		perm := "allow"
		if !p.Enabled {
			perm = "deny"
		}

		views[i] = permissionView{
			Server:     p.Server,
			Tool:       p.ToolName,
			Permission: perm,
		}
	}

	result := resultView{
		Namespace:     namespaceName,
		DenyByDefault: ns.DenyByDefault,
		Permissions:   views,
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func outputPermissionsTable(namespaceName string, ns *config.NamespaceConfig, permissions []config.ToolPermission) error {
	// Print namespace info
	denyDefault := "no"
	if ns.DenyByDefault {
		denyDefault = "yes"
	}
	fmt.Printf("Namespace: %s (deny-by-default: %s)\n\n", namespaceName, denyDefault)

	if len(permissions) == 0 {
		fmt.Println("No explicit permissions configured")
		return nil
	}

	// Calculate column widths
	serverWidth := 6
	toolWidth := 4

	for _, p := range permissions {
		if len(p.Server) > serverWidth {
			serverWidth = len(p.Server)
		}
		if len(p.ToolName) > toolWidth {
			toolWidth = len(p.ToolName)
		}
	}

	// Print header
	fmt.Printf("%-*s  %-*s  %s\n", serverWidth, "SERVER", toolWidth, "TOOL", "PERMISSION")

	// Print permissions
	for _, p := range permissions {
		perm := "allow"
		if !p.Enabled {
			perm = "deny"
		}

		fmt.Printf("%-*s  %-*s  %s\n", serverWidth, p.Server, toolWidth, p.ToolName, perm)
	}

	return nil
}

// normalizeToolName strips a qualified server prefix when it matches the
// selected server. This allows users to paste tools/list output (serverName.tool)
// while preserving legitimate tool names that include dots.
func normalizeToolName(toolName, serverName string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return toolName
	}

	parts := strings.SplitN(toolName, ".", 2)
	if len(parts) != 2 {
		return toolName
	}

	prefix := parts[0]
	if serverName != "" && prefix == serverName {
		return parts[1]
	}

	return toolName
}
