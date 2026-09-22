package main

import (
	"fmt"
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

Changing a feature changes what mcpmu declares to the server at initialize, so
a running instance restarts.

Examples:
  mcpmu server set-client-feature browser elicitation on
  mcpmu server set-client-feature browser elicitation off`,
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
