package server

import (
	"strings"
)

// ToolClassification represents the safety classification of a tool.
type ToolClassification int

const (
	// ToolSafe indicates a read-only operation.
	ToolSafe ToolClassification = iota
	// ToolUnsafe indicates a mutating operation.
	ToolUnsafe
	// ToolUnknown indicates the classification couldn't be determined.
	ToolUnknown
)

// String returns a string representation of the classification.
func (c ToolClassification) String() string {
	switch c {
	case ToolSafe:
		return "safe"
	case ToolUnsafe:
		return "unsafe"
	default:
		return "unknown"
	}
}

// Safe patterns indicate read-only operations.
var safePatterns = []string{
	"read", "get", "list", "search", "view", "show", "describe",
	"fetch", "query", "find", "lookup", "check", "info", "status",
	"count", "exists", "is_", "has_", "can_", "validate",
}

// Unsafe patterns indicate mutating operations.
var unsafePatterns = []string{
	"write", "update", "delete", "execute", "run", "create", "set",
	"modify", "remove", "post", "put", "patch", "send", "invoke",
	"start", "stop", "kill", "terminate", "restart", "reboot",
	"install", "uninstall", "enable", "disable", "add", "drop",
	"truncate", "clear", "reset", "init", "apply", "deploy",
	"publish", "submit", "approve", "reject", "close", "open",
	"lock", "unlock", "grant", "revoke", "move", "rename", "copy",
}

// ClassifyTool classifies a tool based on its name.
// The tool name should be unqualified (without server prefix).
//
// If the input has a server prefix (e.g., "filesystem.read_file"),
// it is automatically stripped before classification.
func ClassifyTool(toolName string) ToolClassification {
	// Strip server prefix if present
	name := stripServerPrefix(toolName)

	// Convert to lowercase for case-insensitive matching
	lower := strings.ToLower(name)

	// Check unsafe patterns first (more restrictive)
	for _, pattern := range unsafePatterns {
		if strings.Contains(lower, pattern) {
			return ToolUnsafe
		}
	}

	// Check safe patterns
	for _, pattern := range safePatterns {
		if strings.Contains(lower, pattern) {
			return ToolSafe
		}
	}

	return ToolUnknown
}

// stripServerPrefix removes the server prefix from a qualified tool name.
// "filesystem.read_file" → "read_file"
// "read_file" → "read_file" (unchanged)
func stripServerPrefix(qualifiedName string) string {
	if idx := strings.Index(qualifiedName, "."); idx != -1 {
		return qualifiedName[idx+1:]
	}
	return qualifiedName
}

// IsSafe returns true if the tool is classified as safe.
func IsSafe(toolName string) bool {
	return ClassifyTool(toolName) == ToolSafe
}

// IsUnsafe returns true if the tool is classified as unsafe.
func IsUnsafe(toolName string) bool {
	return ClassifyTool(toolName) == ToolUnsafe
}
