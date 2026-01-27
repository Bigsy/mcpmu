// Package config provides configuration schema and persistence for MCP Studio.
package config

import (
	"encoding/json"
	"time"
)

// SchemaVersion is the current config schema version.
const SchemaVersion = 1

// ServerKind represents the transport type for an MCP server.
type ServerKind string

const (
	ServerKindStdio ServerKind = "stdio"
	ServerKindSSE   ServerKind = "sse"
)

// ServerConfig represents an MCP server configuration.
// Field names are compatible with mcpServers format for easy copy/paste.
type ServerConfig struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Kind      ServerKind        `json:"kind"`
	Enabled   *bool             `json:"enabled,omitempty"`   // nil treated as true (enabled by default)
	Autostart bool              `json:"autostart,omitempty"` // start server automatically on app launch
	Command   string            `json:"command,omitempty"`   // stdio only
	Args      []string          `json:"args,omitempty"`      // stdio only
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`

	// SSE-specific fields (Phase 5)
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// OAuth fields (Phase 5)
	OAuthEnabled      bool   `json:"oauthEnabled,omitempty"`
	OAuthClientID     string `json:"oauthClientId,omitempty"`
	OAuthScopes       string `json:"oauthScopes,omitempty"`
	OAuthAuthURL      string `json:"oauthAuthUrl,omitempty"`
	OAuthTokenURL     string `json:"oauthTokenUrl,omitempty"`
	OAuthClientSecret string `json:"-"` // Never persisted in config, stored separately
}

// NamespaceConfig represents a namespace that groups servers and their tool permissions.
type NamespaceConfig struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	ServerIDs   []string `json:"serverIds"`
}

// ProxyConfig represents an HTTP proxy configuration (Phase 4, deferred).
// Included in schema for forward compatibility.
type ProxyConfig struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	PathSegment   string `json:"pathSegment"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	TransportType string `json:"transportType"` // "sse" or "streamable"
}

// ToolPermission controls whether a specific tool is enabled in a namespace.
type ToolPermission struct {
	NamespaceID string `json:"namespaceId"`
	ServerID    string `json:"serverId"`
	ToolName    string `json:"toolName"`
	Enabled     bool   `json:"enabled"`
}

// Config is the root configuration structure.
type Config struct {
	SchemaVersion      int                     `json:"schemaVersion"`
	DefaultNamespaceID string                  `json:"defaultNamespaceId,omitempty"`
	Servers            map[string]ServerConfig `json:"servers"`
	Namespaces         []NamespaceConfig       `json:"namespaces,omitempty"`
	Proxies            []ProxyConfig           `json:"proxies,omitempty"`
	ToolPermissions    []ToolPermission        `json:"toolPermissions,omitempty"`
	LastModified       time.Time               `json:"lastModified"`
}

// NewConfig creates a new empty configuration with default values.
func NewConfig() *Config {
	return &Config{
		SchemaVersion: SchemaVersion,
		Servers:       make(map[string]ServerConfig),
		Namespaces:    []NamespaceConfig{},
		Proxies:       []ProxyConfig{},
		LastModified:  time.Now(),
	}
}

// IsEnabled returns whether the server is enabled (nil defaults to true).
func (s ServerConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// SetEnabled sets the enabled state.
func (s *ServerConfig) SetEnabled(enabled bool) {
	s.Enabled = &enabled
}

// ServerList returns the servers as a slice, sorted by name for display.
func (c *Config) ServerList() []ServerConfig {
	servers := make([]ServerConfig, 0, len(c.Servers))
	for _, s := range c.Servers {
		servers = append(servers, s)
	}
	return servers
}

// GetServer returns a server by ID, or nil if not found.
func (c *Config) GetServer(id string) *ServerConfig {
	if s, ok := c.Servers[id]; ok {
		return &s
	}
	return nil
}

// MarshalJSON implements custom JSON marshaling.
func (c *Config) MarshalJSON() ([]byte, error) {
	type Alias Config
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(c),
	})
}
