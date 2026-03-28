package main

import (
	"bufio"
	"fmt"
	"os"
	"slices"
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

func parseBoolFlag(value string, trueValues, falseValues []string, noun, expected string) (bool, error) {
	if slices.Contains(trueValues, value) {
		return true, nil
	}
	if slices.Contains(falseValues, value) {
		return false, nil
	}
	return false, fmt.Errorf("invalid %s %q: expected %s", noun, value, expected)
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
