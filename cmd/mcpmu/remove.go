package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/spf13/cobra"
)

var (
	removeYes        bool
	removeConfigPath string
)

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
	removeCmd.Flags().StringVarP(&removeConfigPath, "config", "c", "", "Path to config file (default: ~/.config/mcpmu/config.json)")

	rootCmd.AddCommand(removeCmd)
}

func runRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Load config
	var cfg *config.Config
	var err error
	if removeConfigPath != "" {
		cfg, err = config.LoadFrom(removeConfigPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Check server exists
	srv := cfg.FindServerByName(name)
	if srv == nil {
		return fmt.Errorf("server %q not found", name)
	}

	// Confirm unless --yes
	if !removeYes {
		fmt.Printf("Remove server %q? [y/N] ", name)
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

	// Remove server
	if err := cfg.DeleteServerByName(name); err != nil {
		return err
	}

	// Save config
	if removeConfigPath != "" {
		if err := config.SaveTo(cfg, removeConfigPath); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
	} else {
		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
	}

	fmt.Printf("Removed server %q\n", name)
	return nil
}
