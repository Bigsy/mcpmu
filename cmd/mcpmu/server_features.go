package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/spf13/cobra"
)

func init() {
	serverCmd.AddCommand(serverSetClientFeatureCmd)
}

var serverSetClientFeatureCmd = &cobra.Command{
	Use:   "set-client-feature <server> <feature> <on|off>",
	Short: "Opt a server in to (or out of) a relayed client feature",
	Long: `Opt a server in to, or out of, a client feature mcpmu relays from the
downstream client in serve mode.

Features:
  elicitation   Declare elicitation upstream and relay elicitation/create to
                the client that caused it. Requests from a shared instance are
                only relayed when they can be tied to one session; set
                "shared": false for certain routing.
  roots         For a private ("shared": false) instance with no configured
                roots, relay roots/list to its owning client and forward that
                client's roots/list_changed. Configured roots always win (see
                "mcpmu server set-roots").
  sampling      Declare sampling upstream and relay sampling/createMessage to
                the client that caused it, letting the server spend that
                client's model tokens. Only relayed from a private instance or
                an HTTP upstream's own response stream — never by heuristic.
  sampling-tools
                Also allow sampling requests that offer the model tools
                (declares sampling.tools; turns sampling on).

Changing a feature changes what mcpmu declares to the server at initialize, so
a running instance restarts.

Examples:
  mcpmu server set-client-feature browser elicitation on
  mcpmu server set-client-feature browser elicitation off
  mcpmu server set-client-feature agent sampling on`,
	Args: cobra.ExactArgs(3),
	RunE: runServerSetClientFeature,
}

func runServerSetClientFeature(cmd *cobra.Command, args []string) error {
	serverName, feature := args[0], strings.ToLower(strings.TrimSpace(args[1]))
	on, err := parseBool(args[2])
	if err != nil {
		return err
	}
	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		if err := requireServer(cfg, serverName); err != nil {
			return err
		}
		srv, _ := cfg.GetServer(serverName)
		if err := srv.SetClientFeature(feature, on); err != nil {
			return err
		}
		return cfg.UpdateServer(serverName, srv)
	}); err != nil {
		return err
	}
	state := "off"
	if on {
		state = "on"
	}
	fmt.Printf("Client feature %s %s for server %q\n", feature, state, serverName)
	return nil
}

func init() {
	serverCmd.AddCommand(serverSetRootsCmd)
}

var serverSetRootsCmd = &cobra.Command{
	Use:   "set-roots <server> [<root>...]",
	Short: "Set the roots mcpmu reports to a server",
	Long: `Set the roots mcpmu reports to a server as its client's roots, replacing
any existing list. Each root is an absolute path or a file:// URI. With no
roots, the list is cleared.

mcpmu answers the server's roots/list itself, for shared and private instances
alike. Editing a non-empty list tells running instances with
notifications/roots/list_changed; adding or clearing the list restarts them,
because it changes what mcpmu declares at initialize.

Examples:
  mcpmu server set-roots filesystem ~/src/app /srv/data
  mcpmu server set-roots filesystem file:///home/me/src/app
  mcpmu server set-roots filesystem`,
	Args: cobra.MinimumNArgs(1),
	RunE: runServerSetRoots,
}

func runServerSetRoots(cmd *cobra.Command, args []string) error {
	serverName := args[0]
	roots, err := normalizeRoots(args[1:])
	if err != nil {
		return err
	}
	if err := mutateConfig(configPath, func(cfg *config.Config) error {
		if err := requireServer(cfg, serverName); err != nil {
			return err
		}
		srv, _ := cfg.GetServer(serverName)
		srv.Roots = roots
		return cfg.UpdateServer(serverName, srv)
	}); err != nil {
		return err
	}
	if len(roots) == 0 {
		fmt.Printf("Cleared roots for server %q\n", serverName)
	} else {
		fmt.Printf("Set %d root(s) for server %q\n", len(roots), serverName)
	}
	return nil
}

// normalizeRoots turns CLI root arguments (absolute paths, ~/ paths or
// file:// URIs) into validated file:// URIs.
func normalizeRoots(args []string) ([]string, error) {
	var roots []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("expand %q: %w", arg, err)
			}
			arg = filepath.Join(home, arg[2:])
		}
		root, err := config.NormalizeRoot(arg)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	if err := config.ValidateRoots(roots); err != nil {
		return nil, err
	}
	return roots, nil
}
