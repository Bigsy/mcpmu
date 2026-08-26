package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
)

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

func confirmAction(msg string) (bool, error) {
	fmt.Printf("%s [y/N] ", msg)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("failed to read response: %w", err)
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes", nil
}

func requireNamespace(cfg *config.Config, name string) error {
	if _, ok := cfg.GetNamespace(name); !ok {
		return fmt.Errorf("namespace %q not found", name)
	}
	return nil
}

func requireServer(cfg *config.Config, name string) error {
	if _, ok := cfg.GetServer(name); !ok {
		return fmt.Errorf("server %q not found", name)
	}
	return nil
}

// parseBool is the one boolean parser for CLI positional values. It accepts
// true/yes/1/allow/on and false/no/0/deny/off, case-insensitively. Every call
// site states what "true" means there (for example `denyByDefault := !allow`)
// so the three permission/namespace commands cannot drift apart again.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "1", "allow", "on":
		return true, nil
	case "false", "no", "0", "deny", "off":
		return false, nil
	}
	return false, fmt.Errorf("invalid value %q: expected true/false (also yes/no, allow/deny, on/off)", s)
}

// mutateConfig applies fn to the config file at configPath (the default path
// when empty) through config.Mutate: locked reload → fn → validate → save, so
// a CLI edit racing a TUI, web or daemon write loses nothing. It is the only
// way a command writes config.
func mutateConfig(configPath string, fn func(*config.Config) error) error {
	_, err := config.Mutate(configPath, fn)
	return err
}
