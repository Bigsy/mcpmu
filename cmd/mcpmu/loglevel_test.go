package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	for in, want := range map[string]logLevel{"debug": levelDebug, "INFO": levelInfo, "Warn": levelWarn, "error": levelError} {
		got, err := parseLogLevel(in)
		if err != nil || got != want {
			t.Errorf("parseLogLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseLogLevel("loud"); err == nil {
		t.Error("parseLogLevel(loud): want error")
	}
}

func TestClassifyLogLine(t *testing.T) {
	cases := map[string]logLevel{
		"Failed to start server foo: exit 1":        levelError,
		"failed to load config":                     levelError,
		"PANIC in handler: nil deref":               levelError,
		"Error: something":                          levelError,
		"Cannot bind :8081":                         levelError,
		"HTTP serve error: listen tcp: in use":      levelError,
		"oauth: failed to refresh token":            levelError,
		"Warning: failed to create tool cache":      levelWarn,
		"No namespace configured; using all":        levelWarn,
		"rejecting request without session":         levelWarn,
		"httpserve: warning: slow client":           levelWarn,
		"Loaded config with 3 servers":              levelInfo,
		"mcpmu serve starting (version=dev)":        levelInfo,
		"Starting server foo":                       levelInfo,
		"EVENT StatusChanged: server=x old=a new=b": levelInfo,
	}
	for msg, want := range cases {
		if got := classifyLogLine([]byte(msg)); got != want {
			t.Errorf("classifyLogLine(%q) = %v, want %v", msg, got, want)
		}
	}
}

// TestLeveledWriterFiltersThroughStdLogger drives the real standard logger
// so the prefix stripping is exercised with what log actually emits.
func TestLeveledWriterFiltersThroughStdLogger(t *testing.T) {
	emit := func(level string) string {
		var buf bytes.Buffer
		if err := configureLogging(level, &buf); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			log.SetOutput(&bytes.Buffer{})
			log.SetFlags(log.LstdFlags)
		})
		log.Printf("mcpmu serve starting (version=%s)", "test")
		log.Printf("Warning: tool cache unavailable")
		log.Printf("Failed to start server %s: %v", "foo", "boom")
		log.Println("Stopping all servers...")
		return buf.String()
	}

	t.Run("error keeps only failures", func(t *testing.T) {
		out := emit("error")
		if !strings.Contains(out, "Failed to start server foo") {
			t.Fatalf("error level dropped the failure:\n%s", out)
		}
		for _, drop := range []string{"starting", "Warning", "Stopping"} {
			if strings.Contains(out, drop) {
				t.Errorf("error level kept %q:\n%s", drop, out)
			}
		}
	})
	t.Run("warn adds warnings but not info", func(t *testing.T) {
		out := emit("warn")
		for _, keep := range []string{"Failed to start", "Warning:"} {
			if !strings.Contains(out, keep) {
				t.Errorf("warn level dropped %q:\n%s", keep, out)
			}
		}
		for _, drop := range []string{"starting", "Stopping"} {
			if strings.Contains(out, drop) {
				t.Errorf("warn level kept %q:\n%s", drop, out)
			}
		}
	})
	t.Run("info keeps everything", func(t *testing.T) {
		out := emit("info")
		if n := strings.Count(out, "\n"); n != 4 {
			t.Fatalf("info level wrote %d lines, want 4:\n%s", n, out)
		}
		if strings.Contains(out, "loglevel_test.go") {
			t.Fatalf("info level must not carry file:line prefix:\n%s", out)
		}
	})
	t.Run("debug adds file:line and payload switches", func(t *testing.T) {
		out := emit("debug")
		if !strings.Contains(out, "loglevel_test.go:") {
			t.Fatalf("debug level missing file:line prefix:\n%s", out)
		}
		if n := strings.Count(out, "\n"); n != 4 {
			t.Fatalf("debug level wrote %d lines, want 4:\n%s", n, out)
		}
	})
}
