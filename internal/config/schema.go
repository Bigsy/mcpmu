// Package config provides configuration schema and persistence for mcpmu.
package config

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the current config schema version.
const SchemaVersion = 1

// ServerKind represents the transport type for an MCP server.
type ServerKind string

const (
	ServerKindStdio          ServerKind = "stdio"
	ServerKindStreamableHTTP ServerKind = "streamable_http"
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

	// Streamable HTTP fields (mutually exclusive with Command)
	URL               string            `json:"url,omitempty"`                 // Server URL for HTTP transport
	BearerTokenEnvVar string            `json:"bearer_token_env_var,omitempty"` // Env var containing bearer token
	HTTPHeaders       map[string]string `json:"http_headers,omitempty"`         // Static HTTP headers
	EnvHTTPHeaders    map[string]string `json:"env_http_headers,omitempty"`     // HTTP headers from env vars (key=header name, value=env var name)
	Scopes            []string          `json:"scopes,omitempty"`               // OAuth scopes to request

	// Timeouts (seconds)
	StartupTimeoutSec int `json:"startup_timeout_sec,omitempty"` // Default 10
	ToolTimeoutSec    int `json:"tool_timeout_sec,omitempty"`    // Default 60
}

// NamespaceConfig represents a namespace that groups servers and their tool permissions.
type NamespaceConfig struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	ServerIDs     []string `json:"serverIds"`
	DenyByDefault bool     `json:"denyByDefault,omitempty"` // If true, unconfigured tools are denied
}

// ProxyConfig represents a proxy configuration (Phase 5, deferred - see plan5.md).
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

	// OAuth settings (Codex-compatible)
	MCPOAuthCredentialStore string `json:"mcp_oauth_credentials_store,omitempty"` // "auto", "keyring", "file"
	MCPOAuthCallbackPort    *int   `json:"mcp_oauth_callback_port,omitempty"`     // nil = random, 0 invalid
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

// IsHTTP returns true if this server uses HTTP transport (has URL configured).
func (s ServerConfig) IsHTTP() bool {
	return s.URL != ""
}

// GetKind returns the effective kind based on configuration.
// If URL is set, returns ServerKindStreamableHTTP regardless of Kind field.
func (s ServerConfig) GetKind() ServerKind {
	if s.URL != "" {
		return ServerKindStreamableHTTP
	}
	if s.Kind == "" {
		return ServerKindStdio
	}
	return s.Kind
}

// StartupTimeout returns the startup timeout in seconds, with a default of 10.
func (s ServerConfig) StartupTimeout() int {
	if s.StartupTimeoutSec <= 0 {
		return 10
	}
	return s.StartupTimeoutSec
}

// ToolTimeout returns the tool call timeout in seconds, with a default of 60.
func (s ServerConfig) ToolTimeout() int {
	if s.ToolTimeoutSec <= 0 {
		return 60
	}
	return s.ToolTimeoutSec
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

	// Keep list ordering stable across runs (maps are randomized).
	sort.SliceStable(servers, func(i, j int) bool {
		key := func(s ServerConfig) string {
			if s.Name != "" {
				return strings.ToLower(s.Name)
			}
			if s.Command != "" {
				return strings.ToLower(s.Command)
			}
			return strings.ToLower(s.ID)
		}
		ki := key(servers[i])
		kj := key(servers[j])
		if ki == kj {
			return servers[i].ID < servers[j].ID
		}
		return ki < kj
	})

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
