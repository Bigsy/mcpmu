// Package config provides configuration schema and persistence for mcpmu.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// OAuthConfig holds per-server OAuth configuration.
type OAuthConfig struct {
	ClientID     string   `json:"client_id,omitempty"`
	ClientSecret string   `json:"client_secret,omitempty"`
	CallbackPort *int     `json:"callback_port,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// ServerConfig represents an MCP server configuration.
// Field names are compatible with mcpServers format (Claude Desktop, Cursor, etc).
// The server name/identifier is the map key, not stored in this struct.
type ServerConfig struct {
	Kind      ServerKind        `json:"kind,omitempty"`      // optional, inferred from command vs url
	Enabled   *bool             `json:"enabled,omitempty"`   // nil treated as true (enabled by default)
	Autostart bool              `json:"autostart,omitempty"` // start server automatically on app launch
	Command   string            `json:"command,omitempty"`   // stdio only
	Args      []string          `json:"args,omitempty"`      // stdio only
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`

	// Streamable HTTP fields (mutually exclusive with Command)
	URL               string            `json:"url,omitempty"`                  // Server URL for HTTP transport
	BearerTokenEnvVar string            `json:"bearer_token_env_var,omitempty"` // Env var containing bearer token
	HTTPHeaders       map[string]string `json:"http_headers,omitempty"`         // Static HTTP headers
	EnvHTTPHeaders    map[string]string `json:"env_http_headers,omitempty"`     // HTTP headers from env vars (key=header name, value=env var name)
	OAuth             *OAuthConfig      `json:"oauth,omitempty"`                // OAuth configuration (HTTP only)

	// Timeouts (seconds)
	StartupTimeoutSec int `json:"startup_timeout_sec,omitempty"` // Default 10
	ToolTimeoutSec    int `json:"tool_timeout_sec,omitempty"`    // Default 60

	// Global deny list — tools listed here are denied regardless of namespace permissions
	DeniedTools []string `json:"deniedTools,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for backward compatibility.
// Migrates old flat fields (scopes, oauth_client_id) into the nested oauth block.
func (s *ServerConfig) UnmarshalJSON(data []byte) error {
	// Alias to avoid recursion
	type Alias ServerConfig

	// Extended type with legacy flat fields
	aux := &struct {
		*Alias
		LegacyScopes        []string `json:"scopes,omitempty"`
		LegacyOAuthClientID string   `json:"oauth_client_id,omitempty"`
	}{
		Alias: (*Alias)(s),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	// Migrate legacy flat fields into OAuth block (nested block takes precedence)
	if aux.LegacyOAuthClientID != "" || len(aux.LegacyScopes) > 0 {
		if s.OAuth == nil {
			s.OAuth = &OAuthConfig{}
		}
		if s.OAuth.ClientID == "" && aux.LegacyOAuthClientID != "" {
			s.OAuth.ClientID = aux.LegacyOAuthClientID
		}
		if len(s.OAuth.Scopes) == 0 && len(aux.LegacyScopes) > 0 {
			s.OAuth.Scopes = aux.LegacyScopes
		}
	}

	return nil
}

// ServerEntry pairs a server name with its configuration.
// Used for iteration when the name is needed.
type ServerEntry struct {
	Name   string
	Config ServerConfig
}

// NamespaceConfig represents a namespace that groups servers and their tool permissions.
// The namespace name/identifier is the map key, not stored in this struct.
type NamespaceConfig struct {
	Description    string          `json:"description,omitempty"`
	ServerIDs      []string        `json:"serverIds"`
	DenyByDefault  bool            `json:"denyByDefault,omitempty"`  // If true, unconfigured tools are denied
	ServerDefaults map[string]bool `json:"serverDefaults,omitempty"` // Per-server deny-default override (true = deny)
}

// NamespaceEntry pairs a namespace name with its configuration.
// Used for iteration when the name is needed.
type NamespaceEntry struct {
	Name   string
	Config NamespaceConfig
}

// ToolPermission controls whether a specific tool is enabled in a namespace.
type ToolPermission struct {
	Namespace string `json:"namespace"`
	Server    string `json:"server"`
	ToolName  string `json:"toolName"`
	Enabled   bool   `json:"enabled"`
}

// Config is the root configuration structure.
type Config struct {
	SchemaVersion    int                        `json:"schemaVersion"`
	DefaultNamespace string                     `json:"defaultNamespace,omitempty"`
	Servers          map[string]ServerConfig    `json:"servers"`
	Namespaces       map[string]NamespaceConfig `json:"namespaces,omitempty"`
	ToolPermissions  []ToolPermission           `json:"toolPermissions,omitempty"`
	LastModified     time.Time                  `json:"lastModified"`

	// OAuth settings (Codex-compatible)
	MCPOAuthCredentialStore string `json:"mcp_oauth_credentials_store,omitempty"` // "auto", "keyring", "file"
	MCPOAuthCallbackPort    *int   `json:"mcp_oauth_callback_port,omitempty"`     // nil = random, 0 invalid
}

// NewConfig creates a new empty configuration with default values.
func NewConfig() *Config {
	return &Config{
		SchemaVersion: SchemaVersion,
		Servers:       make(map[string]ServerConfig),
		Namespaces:    make(map[string]NamespaceConfig),
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

// IsToolDenied returns whether the tool is in the server's global deny list.
func (s ServerConfig) IsToolDenied(toolName string) bool {
	return slices.Contains(s.DeniedTools, toolName)
}

// SetEnabled sets the enabled state.
func (s *ServerConfig) SetEnabled(enabled bool) {
	s.Enabled = &enabled
}

// Validate checks that the ServerConfig is in a valid state.
// Returns an error if:
// - Both Command and URL are set (mutually exclusive)
// - Neither Command nor URL is set (must have one)
// - Kind is explicitly set but doesn't match the fields
func (s ServerConfig) Validate() error {
	hasCommand := s.Command != ""
	hasURL := s.URL != ""

	// Must have exactly one of Command or URL
	if hasCommand && hasURL {
		return errors.New("cannot set both command and url: stdio and http are mutually exclusive")
	}
	if !hasCommand && !hasURL {
		return errors.New("must set either command (for stdio) or url (for http)")
	}

	// If Kind is explicitly set, it must match the fields
	if s.Kind != "" {
		if s.Kind == ServerKindStdio && hasURL {
			return fmt.Errorf("kind is %q but url is set", s.Kind)
		}
		if s.Kind == ServerKindStreamableHTTP && hasCommand {
			return fmt.Errorf("kind is %q but command is set", s.Kind)
		}
	}

	// Stdio-specific validation
	if hasCommand {
		// Args without command doesn't make sense, but Args with command is fine
		// URL-related fields shouldn't be set
		if s.BearerTokenEnvVar != "" {
			return errors.New("bearer_token_env_var is only valid for http servers")
		}
		if len(s.HTTPHeaders) > 0 {
			return errors.New("http_headers is only valid for http servers")
		}
		if len(s.EnvHTTPHeaders) > 0 {
			return errors.New("env_http_headers is only valid for http servers")
		}
		if s.OAuth != nil {
			return errors.New("oauth is only valid for http servers")
		}
	}

	// HTTP-specific validation
	if hasURL {
		// Command-related fields shouldn't be set
		if len(s.Args) > 0 {
			return errors.New("args is only valid for stdio servers")
		}

		// bearer_token_env_var and oauth are mutually exclusive
		if s.BearerTokenEnvVar != "" && s.OAuth != nil {
			return errors.New("bearer_token_env_var and oauth are mutually exclusive")
		}

		// Validate OAuth callback port if set
		if s.OAuth != nil && s.OAuth.CallbackPort != nil {
			port := *s.OAuth.CallbackPort
			if port < 1 || port > 65535 {
				return fmt.Errorf("oauth callback_port must be 1-65535, got %d", port)
			}
		}
	}

	return nil
}

// ServerEntries returns the servers as name/config pairs, sorted by name for display.
func (c *Config) ServerEntries() []ServerEntry {
	entries := make([]ServerEntry, 0, len(c.Servers))
	for name, cfg := range c.Servers {
		entries = append(entries, ServerEntry{Name: name, Config: cfg})
	}

	// Keep list ordering stable across runs (maps are randomized).
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	return entries
}

// NamespaceEntries returns the namespaces as name/config pairs, sorted by name for display.
func (c *Config) NamespaceEntries() []NamespaceEntry {
	entries := make([]NamespaceEntry, 0, len(c.Namespaces))
	for name, cfg := range c.Namespaces {
		entries = append(entries, NamespaceEntry{Name: name, Config: cfg})
	}

	// Keep list ordering stable across runs (maps are randomized).
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	return entries
}

// GetServer returns a server by name and whether it was found.
func (c *Config) GetServer(name string) (ServerConfig, bool) {
	s, ok := c.Servers[name]
	return s, ok
}

// GetNamespace returns a namespace by name and whether it was found.
func (c *Config) GetNamespace(name string) (NamespaceConfig, bool) {
	ns, ok := c.Namespaces[name]
	return ns, ok
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

// Validate checks that all servers in the config are valid.
// Returns an error describing the first invalid server found.
func (c *Config) Validate() error {
	for name, srv := range c.Servers {
		if err := srv.Validate(); err != nil {
			return fmt.Errorf("server %q: %w", name, err)
		}
	}
	return nil
}
