package config

import (
	"strings"
	"testing"
)

func TestNormalizeRoot(t *testing.T) {
	for _, tc := range []struct {
		in, want, err string
	}{
		{in: "file:///home/me/project", want: "file:///home/me/project"},
		{in: "  /home/me/project  ", want: "file:///home/me/project"},
		{in: "/home/me/my project", want: "file:///home/me/my%20project"},
		{in: "file://localhost/srv", want: "file://localhost/srv"},
		{in: "relative/dir", err: "absolute path"},
		{in: "https://example.com/x", err: "file:// URI"},
		{in: "file://remote-host/x", err: "remote host"},
		{in: "", err: "empty root"},
	} {
		got, err := NormalizeRoot(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("NormalizeRoot(%q) = %q, %v; want error containing %q", tc.in, got, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("NormalizeRoot(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestParseRootLines(t *testing.T) {
	roots, err := ParseRootLines("file:///a\n\n  /b  \n")
	if err != nil || len(roots) != 2 || roots[0] != "file:///a" || roots[1] != "file:///b" {
		t.Fatalf("ParseRootLines = %v, %v", roots, err)
	}
	if FormatRootLines(roots) != "file:///a\nfile:///b" {
		t.Errorf("FormatRootLines = %q", FormatRootLines(roots))
	}
	if _, err := ParseRootLines("/a\nfile:///a"); err == nil {
		t.Error("a duplicate root was accepted")
	}
}

func TestServerValidateRoots(t *testing.T) {
	srv := ServerConfig{Command: "x", Roots: []string{"file:///ok"}}
	if err := srv.Validate(); err != nil {
		t.Fatalf("valid roots rejected: %v", err)
	}
	srv.Roots = []string{"/not/a/uri"}
	if err := srv.Validate(); err == nil || !strings.Contains(err.Error(), "roots") {
		t.Fatalf("a bare path in config was accepted: %v", err)
	}
}
