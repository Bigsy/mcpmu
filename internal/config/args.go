package config

import (
	"errors"
	"strings"
)

// SplitArgs splits a shell-words style argument string into arguments.
// Whitespace separates arguments; single or double quotes group words that
// contain whitespace; a backslash escapes the next character. An unterminated
// quote is an error rather than a silently truncated argument.
//
// This is the one parser behind every free-text "Arguments" field (TUI and web
// forms). strings.Fields is not acceptable there: it cannot express an
// argument containing a space.
func SplitArgs(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}

	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)
	escaped := false
	pending := false // true once current has been started (handles "" as an empty arg)

	for _, r := range s {
		if escaped {
			current.WriteRune(r)
			pending = true
			escaped = false
			continue
		}

		switch {
		case r == '\\':
			escaped = true
		case (r == '"' || r == '\'') && !inQuote:
			inQuote = true
			quoteChar = r
			pending = true
		case r == quoteChar && inQuote:
			inQuote = false
			quoteChar = 0
		case (r == ' ' || r == '\t' || r == '\n') && !inQuote:
			if pending {
				args = append(args, current.String())
				current.Reset()
				pending = false
			}
		default:
			current.WriteRune(r)
			pending = true
		}
	}

	if escaped {
		return nil, errors.New("arguments end with an unfinished backslash escape")
	}
	if inQuote {
		return nil, errors.New("arguments contain an unterminated quote")
	}
	if pending {
		args = append(args, current.String())
	}

	return args, nil
}

// JoinArgs renders arguments as a single string that SplitArgs parses back to
// the same slice. Arguments containing whitespace, quotes or backslashes are
// double-quoted with the quotes and backslashes escaped.
func JoinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}

	parts := make([]string, 0, len(args))
	for _, arg := range args {
		escaped := strings.ReplaceAll(arg, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
		if arg == "" || strings.ContainsAny(arg, " \t\n'\"") {
			parts = append(parts, "\""+escaped+"\"")
		} else {
			parts = append(parts, escaped)
		}
	}
	return strings.Join(parts, " ")
}
