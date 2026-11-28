package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonWriteStallTimeout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   time.Duration
	}{
		{"absent", `{}`, 30 * time.Second},
		{"zero", `{"daemon_write_stall_timeout_sec":0}`, 30 * time.Second},
		{"null", `{"daemon_write_stall_timeout_sec":null}`, 30 * time.Second},
		{"one second", `{"daemon_write_stall_timeout_sec":1}`, time.Second},
		{"largest whole seconds", `{"daemon_write_stall_timeout_sec":9223372036}`, 9223372036 * time.Second},
		{"duration overflow", `{"daemon_write_stall_timeout_sec":9223372037}`, 0},
		{"negative", `{"daemon_write_stall_timeout_sec":-1}`, 0},
		{"fraction", `{"daemon_write_stall_timeout_sec":0.5}`, 0},
		{"duration string", `{"daemon_write_stall_timeout_sec":"30s"}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFrom(path)
			if tc.want == 0 {
				if err == nil || !strings.Contains(err.Error(), "daemon_write_stall_timeout_sec") {
					t.Fatalf("expected field error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.DaemonWriteStallTimeout(); got != tc.want {
				t.Errorf("timeout = %s, want %s", got, tc.want)
			}
		})
	}
}
