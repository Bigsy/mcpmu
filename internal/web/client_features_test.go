package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

// TestElicitationFormSetting: the checkbox turns the client feature on and
// off, an absent field preserves it, and the edit form shows it.
func TestElicitationFormSetting(t *testing.T) {
	for _, tc := range []struct {
		body     string
		existing bool
		want     bool
	}{
		{"elicitation=false&elicitation=true", false, true},
		{"elicitation=false", true, false},
		{"", true, true},
	} {
		req := httptest.NewRequest("POST", "/servers", strings.NewReader("command=fixture&"+tc.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		existing := config.ServerConfig{Command: "fixture"}
		if tc.existing {
			existing.ClientFeatures = &config.ClientFeatures{Elicitation: true}
		}
		got, err := buildServerConfig(parseServerForm(req), &existing)
		if err != nil || got.ElicitationEnabled() != tc.want {
			t.Fatalf("%q: %+v %v", tc.body, got, err)
		}
		if serverFormDataFromConfig("fixture", got).Elicitation != tc.want {
			t.Fatalf("%q: edit form lost the setting", tc.body)
		}
	}
	if newServerFormData().Elicitation {
		t.Fatal("elicitation must default to off")
	}
}
