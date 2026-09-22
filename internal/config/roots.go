package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// ValidateRoots checks that every root is a file:// URI naming an absolute
// path, which is all MCP allows a root to be.
func ValidateRoots(roots []string) error {
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		if err := validateRoot(root); err != nil {
			return err
		}
		if seen[root] {
			return fmt.Errorf("root %q is listed twice", root)
		}
		seen[root] = true
	}
	return nil
}

func validateRoot(root string) error {
	u, err := url.Parse(root)
	if err != nil {
		return fmt.Errorf("root %q is not a valid URI: %w", root, err)
	}
	if u.Scheme != "file" {
		return fmt.Errorf("root %q must be a file:// URI", root)
	}
	if u.Host != "" && u.Host != "localhost" {
		return fmt.Errorf("root %q names a remote host; roots must be local paths", root)
	}
	if !strings.HasPrefix(u.Path, "/") {
		return fmt.Errorf("root %q must name an absolute path", root)
	}
	return nil
}

// NormalizeRoot accepts a file:// URI or an absolute path and returns the
// file:// URI, so the CLI and forms can take either.
func NormalizeRoot(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty root")
	}
	if !strings.Contains(s, "://") {
		if !filepath.IsAbs(s) {
			return "", fmt.Errorf("root %q must be an absolute path or a file:// URI", s)
		}
		path := filepath.ToSlash(filepath.Clean(s))
		if !strings.HasPrefix(path, "/") {
			path = "/" + path // Windows: C:/x → /C:/x
		}
		s = (&url.URL{Scheme: "file", Path: path}).String()
	}
	if err := validateRoot(s); err != nil {
		return "", err
	}
	return s, nil
}

// ParseRootLines reads roots one per line (blank lines ignored), normalizing
// each.
func ParseRootLines(text string) ([]string, error) {
	var roots []string
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		root, err := NormalizeRoot(line)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	if err := ValidateRoots(roots); err != nil {
		return nil, err
	}
	return roots, nil
}

// FormatRootLines renders roots one per line, for a form textarea.
func FormatRootLines(roots []string) string {
	return strings.Join(roots, "\n")
}
