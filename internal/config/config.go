package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	configDir  = ".config/mcp-studio"
	configFile = "config.json"
)

// ConfigPath returns the full path to the config file.
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, configDir, configFile), nil
}

// Load reads the configuration from disk.
// Returns a new empty config if the file doesn't exist.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewConfig(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Initialize maps if nil (for older configs)
	if cfg.Servers == nil {
		cfg.Servers = make(map[string]ServerConfig)
	}

	// Backfill ServerConfig.ID from map keys
	for id, srv := range cfg.Servers {
		if srv.ID == "" {
			srv.ID = id
			cfg.Servers[id] = srv
		}
	}

	return &cfg, nil
}

// Save writes the configuration to disk atomically.
// Uses a temp file + rename pattern for atomic writes.
func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	// Ensure config directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Update timestamp
	cfg.LastModified = time.Now()

	// Marshal with indentation for readability
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Write to temp file first
	tmpFile := path + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpFile, path); err != nil {
		os.Remove(tmpFile) // Clean up temp file on failure
		return fmt.Errorf("rename config: %w", err)
	}

	return nil
}

// GenerateID creates a short unique ID for servers.
// IDs are 4 characters [a-z0-9].
func GenerateID() string {
	bytes := make([]byte, 2)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based
		return fmt.Sprintf("%04x", time.Now().UnixNano()&0xFFFF)
	}
	return hex.EncodeToString(bytes)
}

// ValidateID checks if a server ID is valid.
// IDs must be 4 characters [a-z0-9] and cannot contain '.'.
func ValidateID(id string) error {
	if len(id) != 4 {
		return errors.New("id must be 4 characters")
	}
	if strings.Contains(id, ".") {
		return errors.New("id cannot contain '.'")
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return errors.New("id must contain only [a-z0-9]")
		}
	}
	return nil
}

// AddServer adds a new server to the config, generating an ID if needed.
func (c *Config) AddServer(srv ServerConfig) (string, error) {
	// Generate ID if empty
	if srv.ID == "" {
		for {
			srv.ID = GenerateID()
			if _, exists := c.Servers[srv.ID]; !exists {
				break
			}
		}
	}

	// Validate ID
	if err := ValidateID(srv.ID); err != nil {
		return "", fmt.Errorf("invalid id: %w", err)
	}

	// Check for collision
	if _, exists := c.Servers[srv.ID]; exists {
		return "", fmt.Errorf("server id %q already exists", srv.ID)
	}

	// Set default kind
	if srv.Kind == "" {
		srv.Kind = ServerKindStdio
	}

	c.Servers[srv.ID] = srv
	return srv.ID, nil
}

// UpdateServer updates an existing server configuration.
func (c *Config) UpdateServer(srv ServerConfig) error {
	if _, exists := c.Servers[srv.ID]; !exists {
		return fmt.Errorf("server %q not found", srv.ID)
	}
	c.Servers[srv.ID] = srv
	return nil
}

// DeleteServer removes a server from the config.
func (c *Config) DeleteServer(id string) error {
	if _, exists := c.Servers[id]; !exists {
		return fmt.Errorf("server %q not found", id)
	}
	delete(c.Servers, id)

	// Clean up namespace references
	for i := range c.Namespaces {
		c.Namespaces[i].ServerIDs = removeString(c.Namespaces[i].ServerIDs, id)
	}

	// Clean up tool permissions
	filtered := make([]ToolPermission, 0, len(c.ToolPermissions))
	for _, tp := range c.ToolPermissions {
		if tp.ServerID != id {
			filtered = append(filtered, tp)
		}
	}
	c.ToolPermissions = filtered

	return nil
}

func removeString(slice []string, s string) []string {
	result := make([]string, 0, len(slice))
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}
