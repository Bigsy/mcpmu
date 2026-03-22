package server

import (
	"slices"
	"strings"
	"unicode"
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
	"count", "exists", "is", "has", "can", "validate",
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

	// Tokenize on word boundaries (_, -, camelCase)
	tokens := tokenize(name)

	// Check unsafe patterns first (more restrictive)
	for _, pattern := range unsafePatterns {
		if slices.Contains(tokens, pattern) {
			return ToolUnsafe
		}
	}

	// Check safe patterns
	for _, pattern := range safePatterns {
		if slices.Contains(tokens, pattern) {
			return ToolSafe
		}
	}

	return ToolUnknown
}

// tokenize splits a tool name into lowercase word tokens.
// It splits on underscores, hyphens, and camelCase boundaries.
func tokenize(name string) []string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '-'
	})

	var tokens []string
	for _, part := range parts {
		tokens = append(tokens, splitCamelCase(part)...)
	}
	return tokens
}

// splitCamelCase splits a string on camelCase boundaries and lowercases each token.
//
//	"getUser"       → ["get", "user"]
//	"getHTTPStatus" → ["get", "http", "status"]
//	"compute"       → ["compute"]
func splitCamelCase(s string) []string {
	if s == "" {
		return nil
	}

	runes := []rune(s)
	var tokens []string
	start := 0

	for i := 1; i < len(runes); i++ {
		if unicode.IsUpper(runes[i]) {
			if unicode.IsLower(runes[i-1]) {
				// lowercase→uppercase: "getUser" at 'U'
				tokens = append(tokens, strings.ToLower(string(runes[start:i])))
				start = i
			} else if i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
				// end of uppercase run: "HTTPStatus" at 'S'
				tokens = append(tokens, strings.ToLower(string(runes[start:i])))
				start = i
			}
		}
	}

	tokens = append(tokens, strings.ToLower(string(runes[start:])))
	return tokens
}

// stripServerPrefix removes the server prefix from a qualified tool name.
// "filesystem.read_file" → "read_file"
// "read_file" → "read_file" (unchanged)
func stripServerPrefix(qualifiedName string) string {
	if _, after, ok := strings.Cut(qualifiedName, "."); ok {
		return after
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
