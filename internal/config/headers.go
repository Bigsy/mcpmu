package config

import (
	"fmt"
	"sort"
	"strings"
)

// ParseHeaderLines parses newline-separated "Name: Value" entries.
// Used by CLI (--header), TUI form, and web form.
//
// Rules:
//   - Blank lines and lines whose first non-space character is '#' are ignored.
//   - Each line must contain a ':' separator; everything before is the name,
//     everything after is the value. Only the first ':' is treated as the
//     separator so values may contain ':'.
//   - Name must be a valid HTTP header field token (RFC 7230 §3.2.6).
//   - Neither name nor value may contain CR or LF (header-splitting attack).
//   - Value may not contain other control characters (< 0x20 except space/tab).
//   - Duplicate names within the same input are rejected.
//   - Names and values are trimmed of leading/trailing ASCII whitespace;
//     interior whitespace in values is preserved.
func ParseHeaderLines(s string) (map[string]string, error) {
	out := make(map[string]string)
	lines := strings.Split(s, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("line %d: missing ':' separator", i+1)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" {
			return nil, fmt.Errorf("line %d: empty header name", i+1)
		}
		if !validHeaderName(name) {
			return nil, fmt.Errorf("line %d: invalid header name %q (must be a valid HTTP token)", i+1, name)
		}
		if err := validateHeaderValue(value); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("line %d: duplicate header name %q", i+1, name)
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// FormatHeaderLines renders a map back to one "Name: Value" per line, sorted
// by name for stable diffs in dotfile-synced configs.
func FormatHeaderLines(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(m[name])
	}
	return b.String()
}

// ValidateHeaderMap re-checks a map produced outside ParseHeaderLines (e.g.
// loaded from JSON). It applies the same name/value rules so a hand-edited
// config can't sneak in CRLF or invalid tokens.
func ValidateHeaderMap(m map[string]string) error {
	for name, value := range m {
		if name == "" {
			return fmt.Errorf("empty header name")
		}
		if !validHeaderName(name) {
			return fmt.Errorf("invalid header name %q (must be a valid HTTP token)", name)
		}
		if err := validateHeaderValue(value); err != nil {
			return fmt.Errorf("header %q: %w", name, err)
		}
	}
	return nil
}

// validHeaderName reports whether s is a valid HTTP header field name per
// RFC 7230 §3.2.6 (one or more tchar).
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	// net/http exposes ValidHeaderFieldName via the request canonicalization
	// path, but its public function is httpguts.ValidHeaderFieldName which
	// isn't in the standard library. Mirror the rule here.
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isTchar(c) {
			return false
		}
	}
	return true
}

// isTchar reports whether b is a valid token character (RFC 7230 §3.2.6).
func isTchar(b byte) bool {
	switch {
	case b >= '0' && b <= '9':
		return true
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	}
	switch b {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

// validateHeaderValue rejects values containing CR, LF, or other control
// characters (< 0x20) except for horizontal tab.
func validateHeaderValue(v string) error {
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\r' || c == '\n':
			return fmt.Errorf("header value contains CR or LF (would inject a new header)")
		case c < 0x20 && c != '\t':
			return fmt.Errorf("header value contains control character 0x%02x", c)
		case c == 0x7f:
			return fmt.Errorf("header value contains DEL character")
		}
	}
	return nil
}
