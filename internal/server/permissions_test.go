package server

import (
	"testing"

	"github.com/hedworth/mcp-studio-go/internal/config"
)

func TestPermissionResult_String(t *testing.T) {
	tests := []struct {
		result   PermissionResult
		expected string
	}{
		{PermissionAllow, "allow"},
		{PermissionDeny, "deny"},
		{PermissionDefault, "default"},
	}

	for _, tt := range tests {
		if tt.result.String() != tt.expected {
			t.Errorf("expected %q, got %q", tt.expected, tt.result.String())
		}
	}
}

func TestCheckPermission(t *testing.T) {
	cfg := config.NewConfig()
	cfg.Namespaces = []config.NamespaceConfig{
		{ID: "ns01", Name: "test"},
	}
	cfg.Servers["srv1"] = config.ServerConfig{ID: "srv1", Name: "Server 1"}
	cfg.ToolPermissions = []config.ToolPermission{
		{NamespaceID: "ns01", ServerID: "srv1", ToolName: "allowed_tool", Enabled: true},
		{NamespaceID: "ns01", ServerID: "srv1", ToolName: "denied_tool", Enabled: false},
	}

	tests := []struct {
		name        string
		namespaceID string
		serverID    string
		toolName    string
		expected    PermissionResult
	}{
		{
			name:        "explicit allow",
			namespaceID: "ns01",
			serverID:    "srv1",
			toolName:    "allowed_tool",
			expected:    PermissionAllow,
		},
		{
			name:        "explicit deny",
			namespaceID: "ns01",
			serverID:    "srv1",
			toolName:    "denied_tool",
			expected:    PermissionDeny,
		},
		{
			name:        "no explicit rule",
			namespaceID: "ns01",
			serverID:    "srv1",
			toolName:    "unknown_tool",
			expected:    PermissionDefault,
		},
		{
			name:        "different server",
			namespaceID: "ns01",
			serverID:    "srv2",
			toolName:    "allowed_tool",
			expected:    PermissionDefault,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckPermission(cfg, tt.namespaceID, tt.serverID, tt.toolName)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestIsToolAllowed(t *testing.T) {
	cfg := config.NewConfig()
	cfg.Namespaces = []config.NamespaceConfig{
		{ID: "ns01", Name: "allow-by-default"},
		{ID: "ns02", Name: "deny-by-default", DenyByDefault: true},
	}
	cfg.Servers["srv1"] = config.ServerConfig{ID: "srv1", Name: "Server 1"}
	cfg.ToolPermissions = []config.ToolPermission{
		{NamespaceID: "ns01", ServerID: "srv1", ToolName: "explicitly_allowed", Enabled: true},
		{NamespaceID: "ns01", ServerID: "srv1", ToolName: "explicitly_denied", Enabled: false},
		{NamespaceID: "ns02", ServerID: "srv1", ToolName: "explicitly_allowed", Enabled: true},
		{NamespaceID: "ns02", ServerID: "srv1", ToolName: "explicitly_denied", Enabled: false},
	}

	tests := []struct {
		name        string
		namespaceID string
		serverID    string
		toolName    string
		allowed     bool
		hasReason   bool
	}{
		// No namespace (selection=all) - always allowed
		{
			name:        "no namespace allows all",
			namespaceID: "",
			serverID:    "srv1",
			toolName:    "any_tool",
			allowed:     true,
			hasReason:   false,
		},
		// Allow-by-default namespace
		{
			name:        "allow-by-default: explicit allow",
			namespaceID: "ns01",
			serverID:    "srv1",
			toolName:    "explicitly_allowed",
			allowed:     true,
			hasReason:   false,
		},
		{
			name:        "allow-by-default: explicit deny",
			namespaceID: "ns01",
			serverID:    "srv1",
			toolName:    "explicitly_denied",
			allowed:     false,
			hasReason:   true,
		},
		{
			name:        "allow-by-default: no rule = allowed",
			namespaceID: "ns01",
			serverID:    "srv1",
			toolName:    "unknown_tool",
			allowed:     true,
			hasReason:   false,
		},
		// Deny-by-default namespace
		{
			name:        "deny-by-default: explicit allow",
			namespaceID: "ns02",
			serverID:    "srv1",
			toolName:    "explicitly_allowed",
			allowed:     true,
			hasReason:   false,
		},
		{
			name:        "deny-by-default: explicit deny",
			namespaceID: "ns02",
			serverID:    "srv1",
			toolName:    "explicitly_denied",
			allowed:     false,
			hasReason:   true,
		},
		{
			name:        "deny-by-default: no rule = denied",
			namespaceID: "ns02",
			serverID:    "srv1",
			toolName:    "unknown_tool",
			allowed:     false,
			hasReason:   true,
		},
		// Non-existent namespace (edge case)
		{
			name:        "non-existent namespace allows",
			namespaceID: "nonexistent",
			serverID:    "srv1",
			toolName:    "any_tool",
			allowed:     true,
			hasReason:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, reason := IsToolAllowed(cfg, tt.namespaceID, tt.serverID, tt.toolName)
			if allowed != tt.allowed {
				t.Errorf("expected allowed=%v, got %v", tt.allowed, allowed)
			}
			if tt.hasReason && reason == "" {
				t.Error("expected a reason for denial")
			}
			if !tt.hasReason && reason != "" {
				t.Errorf("expected no reason, got %q", reason)
			}
		})
	}
}
