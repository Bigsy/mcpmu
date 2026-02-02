package server

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestPermissionResult_String(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	cfg := config.NewConfig()
	cfg.Namespaces = map[string]config.NamespaceConfig{
		"test": {},
	}
	cfg.Servers["srv1"] = config.ServerConfig{Command: "echo"}
	cfg.ToolPermissions = []config.ToolPermission{
		{Namespace: "test", Server: "srv1", ToolName: "allowed_tool", Enabled: true},
		{Namespace: "test", Server: "srv1", ToolName: "denied_tool", Enabled: false},
	}

	tests := []struct {
		name          string
		namespaceName string
		serverName    string
		toolName      string
		expected      PermissionResult
	}{
		{
			name:          "explicit allow",
			namespaceName: "test",
			serverName:    "srv1",
			toolName:      "allowed_tool",
			expected:      PermissionAllow,
		},
		{
			name:          "explicit deny",
			namespaceName: "test",
			serverName:    "srv1",
			toolName:      "denied_tool",
			expected:      PermissionDeny,
		},
		{
			name:          "no explicit rule",
			namespaceName: "test",
			serverName:    "srv1",
			toolName:      "unknown_tool",
			expected:      PermissionDefault,
		},
		{
			name:          "different server",
			namespaceName: "test",
			serverName:    "srv2",
			toolName:      "allowed_tool",
			expected:      PermissionDefault,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckPermission(cfg, tt.namespaceName, tt.serverName, tt.toolName)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestIsToolAllowed(t *testing.T) {
	t.Parallel()
	cfg := config.NewConfig()
	cfg.Namespaces = map[string]config.NamespaceConfig{
		"allow-by-default": {},
		"deny-by-default":  {DenyByDefault: true},
	}
	cfg.Servers["srv1"] = config.ServerConfig{Command: "echo"}
	cfg.ToolPermissions = []config.ToolPermission{
		{Namespace: "allow-by-default", Server: "srv1", ToolName: "explicitly_allowed", Enabled: true},
		{Namespace: "allow-by-default", Server: "srv1", ToolName: "explicitly_denied", Enabled: false},
		{Namespace: "deny-by-default", Server: "srv1", ToolName: "explicitly_allowed", Enabled: true},
		{Namespace: "deny-by-default", Server: "srv1", ToolName: "explicitly_denied", Enabled: false},
	}

	tests := []struct {
		name          string
		namespaceName string
		serverName    string
		toolName      string
		allowed       bool
		hasReason     bool
	}{
		// No namespace (selection=all) - always allowed
		{
			name:          "no namespace allows all",
			namespaceName: "",
			serverName:    "srv1",
			toolName:      "any_tool",
			allowed:       true,
			hasReason:     false,
		},
		// Allow-by-default namespace
		{
			name:          "allow-by-default: explicit allow",
			namespaceName: "allow-by-default",
			serverName:    "srv1",
			toolName:      "explicitly_allowed",
			allowed:       true,
			hasReason:     false,
		},
		{
			name:          "allow-by-default: explicit deny",
			namespaceName: "allow-by-default",
			serverName:    "srv1",
			toolName:      "explicitly_denied",
			allowed:       false,
			hasReason:     true,
		},
		{
			name:          "allow-by-default: no rule = allowed",
			namespaceName: "allow-by-default",
			serverName:    "srv1",
			toolName:      "unknown_tool",
			allowed:       true,
			hasReason:     false,
		},
		// Deny-by-default namespace
		{
			name:          "deny-by-default: explicit allow",
			namespaceName: "deny-by-default",
			serverName:    "srv1",
			toolName:      "explicitly_allowed",
			allowed:       true,
			hasReason:     false,
		},
		{
			name:          "deny-by-default: explicit deny",
			namespaceName: "deny-by-default",
			serverName:    "srv1",
			toolName:      "explicitly_denied",
			allowed:       false,
			hasReason:     true,
		},
		{
			name:          "deny-by-default: no rule = denied",
			namespaceName: "deny-by-default",
			serverName:    "srv1",
			toolName:      "unknown_tool",
			allowed:       false,
			hasReason:     true,
		},
		// Non-existent namespace (edge case)
		{
			name:          "non-existent namespace allows",
			namespaceName: "nonexistent",
			serverName:    "srv1",
			toolName:      "any_tool",
			allowed:       true,
			hasReason:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, reason := IsToolAllowed(cfg, tt.namespaceName, tt.serverName, tt.toolName)
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
