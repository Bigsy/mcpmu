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
// 2. No explicit entry → return Default (caller should check namespace DenyByDefault)
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
// the namespace's DenyByDefault setting.
//
// Evaluation order:
// 1. If no namespace (namespaceName empty), allow all
// 2. Check explicit ToolPermission → use it
// 3. No explicit entry → check namespace DenyByDefault
// 4. If DenyByDefault=true → deny
// 5. Otherwise → allow
func IsToolAllowed(cfg *config.Config, namespaceName, serverName, toolName string) (bool, string) {
	// No namespace means no permission enforcement
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
		// PermissionDefault - check namespace setting
		if ns.DenyByDefault {
			return false, "tool is not explicitly allowed and namespace denies by default"
		}
		return true, ""
	}
}
