package testutil

import "regexp"

// ansiRegex matches ANSI escape codes.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// StripANSI removes ANSI escape codes from a string.
// Useful for testing TUI output where lipgloss adds styling.
func StripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}
