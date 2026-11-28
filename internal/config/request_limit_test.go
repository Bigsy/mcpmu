package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestStdioRequestLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   int
	}{
		{"absent", `{}`, 64},
		{"minimum", `{"maxConcurrentRequestsPerSession":1}`, 1},
		{"maximum", `{"maxConcurrentRequestsPerSession":4096}`, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if err := json.Unmarshal([]byte(tc.config), &cfg); err != nil {
				t.Fatal(err)
			}
			got, err := cfg.MaxConcurrentRequestsPerSession.Effective()
			if got != tc.want || err != nil {
				t.Fatalf("limit = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestStdioRequestLimitRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"4097", "0", "null", "-1", "1.5", `"2"`} {
		t.Run("value="+value, func(t *testing.T) {
			data := fmt.Sprintf(`{"maxConcurrentRequestsPerSession":%s}`, value)
			var cfg Config
			err := json.Unmarshal([]byte(data), &cfg)
			if err == nil || !strings.Contains(err.Error(), "maxConcurrentRequestsPerSession") {
				t.Fatalf("expected field error, got %v", err)
			}
		})
	}
}
