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

	// Backfill ServerConfig.ID from map keys
	for id, srv := range cfg.Servers {
		if srv.ID == "" {
			srv.ID = id
			cfg.Servers[id] = srv
		}
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
// Returns an error if a server with the same name already exists.
func (c *Config) AddServer(srv ServerConfig) (string, error) {
	// Check for duplicate name
	if srv.Name != "" {
		if existing := c.FindServerByName(srv.Name); existing != nil {
			return "", fmt.Errorf("server with name %q already exists", srv.Name)
		}
	}

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

	// Check for ID collision
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

// FindServerByName returns the server with the given name, or nil if not found.
func (c *Config) FindServerByName(name string) *ServerConfig {
	for _, srv := range c.Servers {
		if srv.Name == name {
			return &srv
		}
	}
	return nil
}

// DeleteServerByName removes a server by name.
// Returns an error if no server with that name exists.
func (c *Config) DeleteServerByName(name string) error {
	for id, srv := range c.Servers {
		if srv.Name == name {
			return c.DeleteServer(id)
		}
	}
	return fmt.Errorf("server %q not found", name)
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

// FindNamespaceByName returns the namespace with the given name, or nil if not found.
func (c *Config) FindNamespaceByName(name string) *NamespaceConfig {
	for i := range c.Namespaces {
		if c.Namespaces[i].Name == name {
			return &c.Namespaces[i]
		}
	}
	return nil
}

// FindNamespaceByID returns the namespace with the given ID, or nil if not found.
func (c *Config) FindNamespaceByID(id string) *NamespaceConfig {
	for i := range c.Namespaces {
		if c.Namespaces[i].ID == id {
			return &c.Namespaces[i]
		}
	}
	return nil
}

// AddNamespace adds a new namespace to the config, generating an ID if needed.
// Returns an error if a namespace with the same name already exists.
func (c *Config) AddNamespace(ns NamespaceConfig) (string, error) {
	// Check for duplicate name
	if ns.Name != "" {
		if existing := c.FindNamespaceByName(ns.Name); existing != nil {
			return "", fmt.Errorf("namespace with name %q already exists", ns.Name)
		}
	}

	// Generate ID if empty
	if ns.ID == "" {
		for {
			ns.ID = GenerateID()
			if c.FindNamespaceByID(ns.ID) == nil {
				break
			}
		}
	}

	// Validate ID
	if err := ValidateID(ns.ID); err != nil {
		return "", fmt.Errorf("invalid id: %w", err)
	}

	// Check for ID collision
	if c.FindNamespaceByID(ns.ID) != nil {
		return "", fmt.Errorf("namespace id %q already exists", ns.ID)
	}

	// Initialize ServerIDs if nil
	if ns.ServerIDs == nil {
		ns.ServerIDs = []string{}
	}

	c.Namespaces = append(c.Namespaces, ns)
	return ns.ID, nil
}

// DeleteNamespaceByName removes a namespace by name.
// Returns an error if no namespace with that name exists.
// Also cleans up tool permissions and default namespace reference.
func (c *Config) DeleteNamespaceByName(name string) error {
	for i, ns := range c.Namespaces {
		if ns.Name == name {
			return c.deleteNamespace(i, ns.ID)
		}
	}
	return fmt.Errorf("namespace %q not found", name)
}

// DeleteNamespaceByID removes a namespace by ID.
func (c *Config) DeleteNamespaceByID(id string) error {
	for i, ns := range c.Namespaces {
		if ns.ID == id {
			return c.deleteNamespace(i, id)
		}
	}
	return fmt.Errorf("namespace %q not found", id)
}

// deleteNamespace removes namespace at index and cleans up references.
func (c *Config) deleteNamespace(index int, id string) error {
	// Remove from slice
	c.Namespaces = append(c.Namespaces[:index], c.Namespaces[index+1:]...)

	// Clean up tool permissions
	filtered := make([]ToolPermission, 0, len(c.ToolPermissions))
	for _, tp := range c.ToolPermissions {
		if tp.NamespaceID != id {
			filtered = append(filtered, tp)
		}
	}
	c.ToolPermissions = filtered

	// Clear default namespace if it was this one
	if c.DefaultNamespaceID == id {
		c.DefaultNamespaceID = ""
	}

	return nil
}

// UpdateNamespace updates an existing namespace configuration.
func (c *Config) UpdateNamespace(ns NamespaceConfig) error {
	for i := range c.Namespaces {
		if c.Namespaces[i].ID == ns.ID {
			c.Namespaces[i] = ns
			return nil
		}
	}
	return fmt.Errorf("namespace %q not found", ns.ID)
}

// AssignServerToNamespace adds a server to a namespace's server list.
func (c *Config) AssignServerToNamespace(namespaceID, serverID string) error {
	ns := c.FindNamespaceByID(namespaceID)
	if ns == nil {
		return fmt.Errorf("namespace %q not found", namespaceID)
	}

	// Check server exists
	if c.GetServer(serverID) == nil {
		return fmt.Errorf("server %q not found", serverID)
	}

	// Check if already assigned
	for _, sid := range ns.ServerIDs {
		if sid == serverID {
			return nil // Already assigned, no-op
		}
	}

	ns.ServerIDs = append(ns.ServerIDs, serverID)
	return nil
}

// UnassignServerFromNamespace removes a server from a namespace's server list.
func (c *Config) UnassignServerFromNamespace(namespaceID, serverID string) error {
	ns := c.FindNamespaceByID(namespaceID)
	if ns == nil {
		return fmt.Errorf("namespace %q not found", namespaceID)
	}

	ns.ServerIDs = removeString(ns.ServerIDs, serverID)
	return nil
}

// SetToolPermission sets a permission for a tool in a namespace.
// If a permission already exists, it is updated.
func (c *Config) SetToolPermission(namespaceID, serverID, toolName string, enabled bool) error {
	// Validate namespace exists
	if c.FindNamespaceByID(namespaceID) == nil {
		return fmt.Errorf("namespace %q not found", namespaceID)
	}

	// Validate server exists
	if c.GetServer(serverID) == nil {
		return fmt.Errorf("server %q not found", serverID)
	}

	// Check if permission already exists
	for i := range c.ToolPermissions {
		tp := &c.ToolPermissions[i]
		if tp.NamespaceID == namespaceID && tp.ServerID == serverID && tp.ToolName == toolName {
			tp.Enabled = enabled
			return nil
		}
	}

	// Add new permission
	c.ToolPermissions = append(c.ToolPermissions, ToolPermission{
		NamespaceID: namespaceID,
		ServerID:    serverID,
		ToolName:    toolName,
		Enabled:     enabled,
	})
	return nil
}

// UnsetToolPermission removes a permission for a tool, reverting to namespace default.
func (c *Config) UnsetToolPermission(namespaceID, serverID, toolName string) error {
	for i, tp := range c.ToolPermissions {
		if tp.NamespaceID == namespaceID && tp.ServerID == serverID && tp.ToolName == toolName {
			c.ToolPermissions = append(c.ToolPermissions[:i], c.ToolPermissions[i+1:]...)
			return nil
		}
	}
	return nil // Not found is not an error
}

// GetToolPermission returns the explicit permission for a tool, if any.
// Returns (permission, found).
func (c *Config) GetToolPermission(namespaceID, serverID, toolName string) (bool, bool) {
	for _, tp := range c.ToolPermissions {
		if tp.NamespaceID == namespaceID && tp.ServerID == serverID && tp.ToolName == toolName {
			return tp.Enabled, true
		}
	}
	return false, false
}

// GetToolPermissionsForNamespace returns all tool permissions for a namespace.
func (c *Config) GetToolPermissionsForNamespace(namespaceID string) []ToolPermission {
	result := []ToolPermission{}
	for _, tp := range c.ToolPermissions {
		if tp.NamespaceID == namespaceID {
			result = append(result, tp)
		}
	}
	return result
}
