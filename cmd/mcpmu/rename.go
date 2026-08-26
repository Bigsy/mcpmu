package main

import (
	"fmt"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/spf13/cobra"
)

var renameCmd = &cobra.Command{
	Use:   "rename <old-name> <new-name>",
	Short: "Rename an MCP server",
	Long: `Rename an MCP server, updating all references.

This atomically updates:
- The server's name in the config
- All namespace references to this server
- All tool permission references to this server

Examples:
  mcpmu rename old-server new-server`,
	Args: cobra.ExactArgs(2),
	RunE: runRename,
}

func init() {
	rootCmd.AddCommand(renameCmd)
}

func runRename(cmd *cobra.Command, args []string) error {
	oldName := args[0]
	newName := args[1]

	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		if err := cfg.RenameServer(oldName, newName); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}

	fmt.Printf("Renamed server %q to %q\n", oldName, newName)
	return nil
}

// Namespace rename subcommand

var namespaceRenameCmd = &cobra.Command{
	Use:   "rename <old-name> <new-name>",
	Short: "Rename a namespace",
	Long: `Rename a namespace, updating all references.

This atomically updates:
- The namespace's name in the config
- The default namespace reference (if it was this namespace)
- All tool permission references to this namespace

Examples:
  mcpmu namespace rename old-namespace new-namespace`,
	Args: cobra.ExactArgs(2),
	RunE: runNamespaceRename,
}

func init() {
	namespaceCmd.AddCommand(namespaceRenameCmd)
}

func runNamespaceRename(cmd *cobra.Command, args []string) error {
	oldName := args[0]
	newName := args[1]

	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		if err := cfg.RenameNamespace(oldName, newName); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}

	fmt.Printf("Renamed namespace %q to %q\n", oldName, newName)
	return nil
}
