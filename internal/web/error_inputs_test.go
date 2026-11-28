package web

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/metrics"
)

func TestErrorInputFormSetting(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"record_error_inputs=false", false}, {"record_error_inputs=false&record_error_inputs=true", true}, {"", true},
	} {
		req := httptest.NewRequest("POST", "/servers", strings.NewReader("command=fixture&"+tc.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		existing := config.ServerConfig{Command: "fixture", RecordErrorInputs: true}
		got, err := buildServerConfig(parseServerForm(req), &existing)
		if err != nil || got.RecordErrorInputs != tc.want {
			t.Fatalf("%s: %+v %v", tc.body, got, err)
		}
		if serverFormDataFromConfig("fixture", got).RecordErrorInputs != tc.want {
			t.Fatal("edit form lost setting")
		}
	}
	if newServerFormData().RecordErrorInputs {
		t.Fatal("capture must default to off")
	}
}

func TestMetricsInputDisplay(t *testing.T) {
	srv := newMetricsTestServer(t)
	rec := metrics.NewRecorder(filepath.Join(filepath.Dir(srv.configPath), "metrics.json"), 60)
	defer func() { _ = rec.Close() }()
	rec.Record(metrics.CallSample{Time: time.Now(), Namespace: "work", Server: "github", Tool: "create_issue", Outcome: metrics.OutcomeError, ErrorInput: json.RawMessage(`{"query":"<script>input</script>","api_key":"input-secret"}`), ErrorResponse: "diagnostic"})
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	status, body := get(t, srv, "/metrics/errors?server=github&tool=create_issue&ns=work")
	if status != 200 {
		t.Fatalf("status: %d", status)
	}
	for _, want := range []string{"<summary>Input</summary>", "<summary>Error response</summary>", "&lt;script&gt;input&lt;/script&gt;", "[REDACTED]", "diagnostic", "No input was recorded"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, forbidden := range []string{"<script>input", "input-secret"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("unsafe output: %s", forbidden)
		}
	}
}

func TestMetricsMissingInputSetting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		removed bool
		want    string
	}{
		{name: "enabled", enabled: true, want: "Input recording is currently enabled for this server."},
		{name: "disabled", want: "Enable “Record inputs for failed tool calls”"},
		{name: "removed", removed: true, want: "Inputs missing from historical errors cannot be recovered."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newMetricsTestServer(t)
			if err := srv.mutateConfig(func(cfg *config.Config) error {
				server := cfg.Servers["github"]
				server.RecordErrorInputs = tc.enabled
				cfg.Servers["github"] = server
				if tc.removed {
					return cfg.DeleteServer("github")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			status, body := get(t, srv, "/metrics/errors?server=github&tool=create_issue&ns=work")
			if status != 200 || !strings.Contains(body, "No input was recorded for this error.") || !strings.Contains(body, tc.want) {
				t.Fatalf("unexpected response (%d): %s", status, body)
			}
			if (tc.enabled || tc.removed) && strings.Contains(body, "Enable “Record inputs for failed tool calls”") {
				t.Fatal("incorrectly prompted to enable input recording")
			}
		})
	}
}
