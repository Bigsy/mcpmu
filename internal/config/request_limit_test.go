package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestStdioRequestLimit(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{
		{"", 64}, {"1", 1}, {"4096", 4096}, {"4097", 0}, {"0", 0},
		{"null", 0}, {"-1", 0}, {"1.5", 0}, {`"2"`, 0},
	} {
		t.Run("value="+tc.input, func(t *testing.T) {
			data := "{}"
			if tc.input != "" {
				data = fmt.Sprintf(`{"maxConcurrentRequestsPerSession":%s}`, tc.input)
			}
			var cfg Config
			err := json.Unmarshal([]byte(data), &cfg)
			if tc.want == 0 {
				if err == nil || !strings.Contains(err.Error(), "maxConcurrentRequestsPerSession") {
					t.Fatalf("expected field error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := cfg.MaxConcurrentRequestsPerSession.Effective()
			if got != tc.want || err != nil {
				t.Fatalf("limit = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}
