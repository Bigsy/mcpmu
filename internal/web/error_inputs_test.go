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
	defer rec.Close()
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
