package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	configDir  = ".config/mcpmu"
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

// Load reads the configuration from the default path.
// Returns a new empty config if the file doesn't exist.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}

// LoadFrom reads the configuration from a specific path.
// Returns a new empty config if the file doesn't exist.
func LoadFrom(path string) (*Config, error) {
	// Expand ~ in path
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		path = filepath.Join(home, path[2:])
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
	if cfg.Namespaces == nil {
		cfg.Namespaces = make(map[string]NamespaceConfig)
	}

	// Validate all servers
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// Save writes the configuration to the default path atomically.
func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	return SaveTo(cfg, path)
}

// SaveTo writes the configuration to a specific path atomically.
// Uses a temp file + rename pattern for atomic writes.
func SaveTo(cfg *Config, path string) error {
	// Expand ~ in path
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home dir: %w", err)
		}
		path = filepath.Join(home, path[2:])
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
		_ = os.Remove(tmpFile) // Clean up temp file on failure
		return fmt.Errorf("rename config: %w", err)
	}

	return nil
}

// ValidateName checks if a server or namespace name is valid.
// Names cannot be empty or contain '.'.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("name cannot be empty")
	}
	if strings.Contains(name, ".") {
		return errors.New("name cannot contain '.'")
	}
	return nil
}

// AddServer adds a new server to the config with the given name.
// Returns an error if a server with that name already exists or if the config is invalid.
func (c *Config) AddServer(name string, srv ServerConfig) error {
	// Validate name
	if err := ValidateName(name); err != nil {
		return fmt.Errorf("invalid name: %w", err)
	}

	// Validate server config
	if err := srv.Validate(); err != nil {
		return fmt.Errorf("invalid server config: %w", err)
	}

	// Check for duplicate name
	if _, exists := c.Servers[name]; exists {
		return fmt.Errorf("server %q already exists", name)
	}

	c.Servers[name] = srv
	return nil
}

// UpdateServer updates an existing server configuration.
func (c *Config) UpdateServer(name string, srv ServerConfig) error {
	if _, exists := c.Servers[name]; !exists {
		return fmt.Errorf("server %q not found", name)
	}

	// Validate server config
	if err := srv.Validate(); err != nil {
		return fmt.Errorf("invalid server config: %w", err)
	}

	c.Servers[name] = srv
	return nil
}

// DeleteServer removes a server from the config by name.
// Also cleans up namespace references and tool permissions.
func (c *Config) DeleteServer(name string) error {
	if _, exists := c.Servers[name]; !exists {
		return fmt.Errorf("server %q not found", name)
	}
	delete(c.Servers, name)

	// Clean up namespace references
	for nsName, ns := range c.Namespaces {
		ns.ServerIDs = removeString(ns.ServerIDs, name)
		c.Namespaces[nsName] = ns
	}

	// Clean up tool permissions
	filtered := make([]ToolPermission, 0, len(c.ToolPermissions))
	for _, tp := range c.ToolPermissions {
		if tp.Server != name {
			filtered = append(filtered, tp)
		}
	}
	c.ToolPermissions = filtered

	return nil
}

// RenameServer renames a server, updating all references atomically.
func (c *Config) RenameServer(oldName, newName string) error {
	srv, exists := c.Servers[oldName]
	if !exists {
		return fmt.Errorf("server %q not found", oldName)
	}
	if _, exists := c.Servers[newName]; exists {
		return fmt.Errorf("server %q already exists", newName)
	}
	if err := ValidateName(newName); err != nil {
		return fmt.Errorf("invalid name: %w", err)
	}

	// Move in servers map
	delete(c.Servers, oldName)
	c.Servers[newName] = srv

	// Update namespace references
	for nsName, ns := range c.Namespaces {
		for i, sid := range ns.ServerIDs {
			if sid == oldName {
				ns.ServerIDs[i] = newName
				c.Namespaces[nsName] = ns
			}
		}
	}

	// Update tool permissions
	for i, tp := range c.ToolPermissions {
		if tp.Server == oldName {
			c.ToolPermissions[i].Server = newName
		}
	}

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

// AddNamespace adds a new namespace to the config with the given name.
// Returns an error if a namespace with that name already exists.
func (c *Config) AddNamespace(name string, ns NamespaceConfig) error {
	// Validate name
	if err := ValidateName(name); err != nil {
		return fmt.Errorf("invalid name: %w", err)
	}

	// Check for duplicate name
	if _, exists := c.Namespaces[name]; exists {
		return fmt.Errorf("namespace %q already exists", name)
	}

	// Initialize ServerIDs if nil
	if ns.ServerIDs == nil {
		ns.ServerIDs = []string{}
	}

	c.Namespaces[name] = ns
	return nil
}

// UpdateNamespace updates an existing namespace configuration.
func (c *Config) UpdateNamespace(name string, ns NamespaceConfig) error {
	if _, exists := c.Namespaces[name]; !exists {
		return fmt.Errorf("namespace %q not found", name)
	}
	c.Namespaces[name] = ns
	return nil
}

// DeleteNamespace removes a namespace by name.
// Also cleans up tool permissions and default namespace reference.
func (c *Config) DeleteNamespace(name string) error {
	if _, exists := c.Namespaces[name]; !exists {
		return fmt.Errorf("namespace %q not found", name)
	}

	delete(c.Namespaces, name)

	// Clean up tool permissions
	filtered := make([]ToolPermission, 0, len(c.ToolPermissions))
	for _, tp := range c.ToolPermissions {
		if tp.Namespace != name {
			filtered = append(filtered, tp)
		}
	}
	c.ToolPermissions = filtered

	// Clear default namespace if it was this one
	if c.DefaultNamespace == name {
		c.DefaultNamespace = ""
	}

	return nil
}

// RenameNamespace renames a namespace, updating all references atomically.
func (c *Config) RenameNamespace(oldName, newName string) error {
	ns, exists := c.Namespaces[oldName]
	if !exists {
		return fmt.Errorf("namespace %q not found", oldName)
	}
	if _, exists := c.Namespaces[newName]; exists {
		return fmt.Errorf("namespace %q already exists", newName)
	}
	if err := ValidateName(newName); err != nil {
		return fmt.Errorf("invalid name: %w", err)
	}

	// Move in namespaces map
	delete(c.Namespaces, oldName)
	c.Namespaces[newName] = ns

	// Update default namespace reference
	if c.DefaultNamespace == oldName {
		c.DefaultNamespace = newName
	}

	// Update tool permissions
	for i, tp := range c.ToolPermissions {
		if tp.Namespace == oldName {
			c.ToolPermissions[i].Namespace = newName
		}
	}

	return nil
}

// AssignServerToNamespace adds a server to a namespace's server list.
func (c *Config) AssignServerToNamespace(namespaceName, serverName string) error {
	ns, exists := c.Namespaces[namespaceName]
	if !exists {
		return fmt.Errorf("namespace %q not found", namespaceName)
	}

	// Check server exists
	if _, ok := c.GetServer(serverName); !ok {
		return fmt.Errorf("server %q not found", serverName)
	}

	// Check if already assigned
	if slices.Contains(ns.ServerIDs, serverName) {
		return nil // Already assigned, no-op
	}

	ns.ServerIDs = append(ns.ServerIDs, serverName)
	c.Namespaces[namespaceName] = ns
	return nil
}

// UnassignServerFromNamespace removes a server from a namespace's server list.
func (c *Config) UnassignServerFromNamespace(namespaceName, serverName string) error {
	ns, exists := c.Namespaces[namespaceName]
	if !exists {
		return fmt.Errorf("namespace %q not found", namespaceName)
	}

	ns.ServerIDs = removeString(ns.ServerIDs, serverName)
	c.Namespaces[namespaceName] = ns
	return nil
}

// SetToolPermission sets a permission for a tool in a namespace.
// If a permission already exists, it is updated.
func (c *Config) SetToolPermission(namespaceName, serverName, toolName string, enabled bool) error {
	// Validate namespace exists
	if _, exists := c.Namespaces[namespaceName]; !exists {
		return fmt.Errorf("namespace %q not found", namespaceName)
	}

	// Validate server exists
	if _, ok := c.GetServer(serverName); !ok {
		return fmt.Errorf("server %q not found", serverName)
	}

	// Check if permission already exists
	for i := range c.ToolPermissions {
		tp := &c.ToolPermissions[i]
		if tp.Namespace == namespaceName && tp.Server == serverName && tp.ToolName == toolName {
			tp.Enabled = enabled
			return nil
		}
	}

	// Add new permission
	c.ToolPermissions = append(c.ToolPermissions, ToolPermission{
		Namespace: namespaceName,
		Server:    serverName,
		ToolName:  toolName,
		Enabled:   enabled,
	})
	return nil
}

// UnsetToolPermission removes a permission for a tool, reverting to namespace default.
func (c *Config) UnsetToolPermission(namespaceName, serverName, toolName string) error {
	for i, tp := range c.ToolPermissions {
		if tp.Namespace == namespaceName && tp.Server == serverName && tp.ToolName == toolName {
			c.ToolPermissions = append(c.ToolPermissions[:i], c.ToolPermissions[i+1:]...)
			return nil
		}
	}
	return nil // Not found is not an error
}

// GetToolPermission returns the explicit permission for a tool, if any.
// Returns (permission, found).
func (c *Config) GetToolPermission(namespaceName, serverName, toolName string) (bool, bool) {
	for _, tp := range c.ToolPermissions {
		if tp.Namespace == namespaceName && tp.Server == serverName && tp.ToolName == toolName {
			return tp.Enabled, true
		}
	}
	return false, false
}

// GetToolPermissionsForNamespace returns all tool permissions for a namespace.
func (c *Config) GetToolPermissionsForNamespace(namespaceName string) []ToolPermission {
	result := []ToolPermission{}
	for _, tp := range c.ToolPermissions {
		if tp.Namespace == namespaceName {
			result = append(result, tp)
		}
	}
	return result
}
