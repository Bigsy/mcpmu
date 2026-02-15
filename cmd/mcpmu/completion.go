package main

import (
	"sort"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/spf13/cobra"
)

func init() {
	// Server commands
	removeCmd.ValidArgsFunction = completeServerNames
	renameCmd.ValidArgsFunction = completeServerNames

	// MCP commands (only HTTP servers are valid)
	mcpLoginCmd.ValidArgsFunction = completeHTTPServerNames
	mcpLogoutCmd.ValidArgsFunction = completeHTTPServerNames

	// Namespace commands (single arg: namespace name)
	namespaceRemoveCmd.ValidArgsFunction = completeNamespaceNames
	namespaceDefaultCmd.ValidArgsFunction = completeNamespaceNames
	namespaceRenameCmd.ValidArgsFunction = completeNamespaceNames

	// Namespace commands (namespace + server)
	namespaceAssignCmd.ValidArgsFunction = completeNamespaceThenServer
	namespaceUnassignCmd.ValidArgsFunction = completeNamespaceThenServer

	// Namespace set-deny-default (namespace + true/false)
	namespaceSetDenyDefaultCmd.ValidArgsFunction = completeNamespaceThenBool

	// Permission commands
	permissionListCmd.ValidArgsFunction = completeNamespaceNames
	permissionSetCmd.ValidArgsFunction = completePermissionSetArgs
	permissionUnsetCmd.ValidArgsFunction = completePermissionUnsetArgs

	// Flag completions
	_ = serveCmd.RegisterFlagCompletionFunc("namespace", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return namespaceNames(cmd), cobra.ShellCompDirectiveNoFileComp
	})
	_ = serveCmd.RegisterFlagCompletionFunc("log-level", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"debug", "info", "warn", "error"}, cobra.ShellCompDirectiveNoFileComp
	})
}

// loadConfigForCompletion loads config silently for shell completion.
func loadConfigForCompletion(cmd *cobra.Command) *config.Config {
	cfgPath, _ := cmd.Root().PersistentFlags().GetString("config")
	var cfg *config.Config
	var err error
	if cfgPath != "" {
		cfg, err = config.LoadFrom(cfgPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return nil
	}
	return cfg
}

func serverNames(cmd *cobra.Command) []string {
	cfg := loadConfigForCompletion(cmd)
	if cfg == nil {
		return nil
	}
	entries := cfg.ServerEntries()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}

func httpServerNames(cmd *cobra.Command) []string {
	cfg := loadConfigForCompletion(cmd)
	if cfg == nil {
		return nil
	}
	entries := cfg.ServerEntries()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	var names []string
	for _, e := range entries {
		if e.Config.IsHTTP() {
			names = append(names, e.Name)
		}
	}
	return names
}

func namespaceNames(cmd *cobra.Command) []string {
	cfg := loadConfigForCompletion(cmd)
	if cfg == nil {
		return nil
	}
	entries := cfg.NamespaceEntries()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}

// completeServerNames completes server names for the first argument.
func completeServerNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return serverNames(cmd), cobra.ShellCompDirectiveNoFileComp
}

// completeHTTPServerNames completes only HTTP server names (for OAuth commands).
func completeHTTPServerNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return httpServerNames(cmd), cobra.ShellCompDirectiveNoFileComp
}

// completeNamespaceNames completes namespace names for the first argument.
func completeNamespaceNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return namespaceNames(cmd), cobra.ShellCompDirectiveNoFileComp
}

// completeNamespaceThenServer completes namespace (arg 0) then server (arg 1).
func completeNamespaceThenServer(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return namespaceNames(cmd), cobra.ShellCompDirectiveNoFileComp
	case 1:
		return serverNames(cmd), cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeNamespaceThenBool completes namespace (arg 0) then true/false (arg 1).
func completeNamespaceThenBool(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return namespaceNames(cmd), cobra.ShellCompDirectiveNoFileComp
	case 1:
		return []string{"true", "false"}, cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// completePermissionSetArgs completes: namespace, server, (no tool completion), allow/deny.
func completePermissionSetArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return namespaceNames(cmd), cobra.ShellCompDirectiveNoFileComp
	case 1:
		return serverNames(cmd), cobra.ShellCompDirectiveNoFileComp
	case 2:
		// Tool names would require starting the server; skip.
		return nil, cobra.ShellCompDirectiveNoFileComp
	case 3:
		return []string{"allow", "deny"}, cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// completePermissionUnsetArgs completes: namespace, server (tool can't be completed).
func completePermissionUnsetArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return namespaceNames(cmd), cobra.ShellCompDirectiveNoFileComp
	case 1:
		return serverNames(cmd), cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}