package server

import (
	"github.com/Bigsy/mcpmu/internal/config"
)

// PermissionResult represents the result of a permission check.
type PermissionResult int

const (
	// PermissionAllow indicates the tool is explicitly allowed.
	PermissionAllow PermissionResult = iota
	// PermissionDeny indicates the tool is explicitly denied.
	PermissionDeny
	// PermissionDefault indicates no explicit rule; use namespace default.
	PermissionDefault
)

// String returns a string representation of the permission result.
func (p PermissionResult) String() string {
	switch p {
	case PermissionAllow:
		return "allow"
	case PermissionDeny:
		return "deny"
	default:
		return "default"
	}
}

// CheckPermission evaluates whether a tool call is allowed.
// Returns PermissionAllow, PermissionDeny, or PermissionDefault.
//
// Evaluation order:
// 1. Check explicit ToolPermission entry → return Allow/Deny
// 2. No explicit entry → return Default (caller applies server default, then namespace DenyByDefault)
func CheckPermission(cfg *config.Config, namespaceName, serverName, toolName string) PermissionResult {
	// Check for explicit permission
	enabled, found := cfg.GetToolPermission(namespaceName, serverName, toolName)
	if found {
		if enabled {
			return PermissionAllow
		}
		return PermissionDeny
	}

	// No explicit rule
	return PermissionDefault
}

// IsToolAllowed checks if a tool call should be allowed, taking into account
// per-server defaults and the namespace's DenyByDefault setting.
//
// Evaluation order:
// 1. If no namespace (namespaceName empty), allow all
// 2. Check explicit ToolPermission → use it
// 3. No explicit entry → check per-server default (ServerDefaults)
// 4. No server default → check namespace DenyByDefault
// 5. If deny → deny; otherwise → allow
func IsToolAllowed(cfg *config.Config, namespaceName, serverName, toolName string) (bool, string) {
	// Check server-level global deny first (applies even without a namespace)
	if srv, ok := cfg.GetServer(serverName); ok && srv.IsToolDenied(toolName) {
		return false, "tool is globally denied on this server"
	}

	// No namespace means no further permission enforcement
	if namespaceName == "" {
		return true, ""
	}

	// Get namespace for DenyByDefault setting
	ns, ok := cfg.GetNamespace(namespaceName)
	if !ok {
		// Namespace not found, allow (shouldn't happen in normal use)
		return true, ""
	}

	// Check permission
	result := CheckPermission(cfg, namespaceName, serverName, toolName)
	switch result {
	case PermissionAllow:
		return true, ""
	case PermissionDeny:
		return false, "tool is explicitly denied in this namespace"
	default:
		// PermissionDefault - check per-server default first
		if serverDefault, found := cfg.GetServerDefault(namespaceName, serverName); found {
			if serverDefault {
				return false, "tool is not explicitly allowed and server denies by default in this namespace"
			}
			return true, ""
		}
		// Fall through to namespace default
		if ns.DenyByDefault {
			return false, "tool is not explicitly allowed and namespace denies by default"
		}
		return true, ""
	}
}
