package main

import (
	"fmt"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/spf13/cobra"
)

var removeYes bool

var removeCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an MCP server",
	Long: `Remove an MCP server from the configuration.

By default, prompts for confirmation. Use --yes to skip the prompt.

Examples:
  mcpmu remove my-server
  mcpmu remove my-server --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runRemove,
}

func init() {
	removeCmd.Flags().BoolVarP(&removeYes, "yes", "y", false, "Skip confirmation prompt")

	rootCmd.AddCommand(removeCmd)
}

func runRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Load config
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	// Check server exists
	if err := requireServer(cfg, name); err != nil {
		return err
	}

	// Confirm unless --yes
	if !removeYes {
		confirmed, err := confirmAction(fmt.Sprintf("Remove server %q?", name))
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Cancelled")
			return nil
		}
	}

	// Remove server (re-checks existence under the lock; the ToolCache entry
	// goes with it)
	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		return cfg.DeleteServer(name)
	}); err != nil {
		return err
	}

	fmt.Printf("Removed server %q\n", name)
	return nil
}
