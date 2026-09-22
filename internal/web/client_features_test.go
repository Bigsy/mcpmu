package web

import (
	"net/http/httptest"
	"net/url"
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

func TestRootsFormSetting(t *testing.T) {
	req := httptest.NewRequest("POST", "/servers", strings.NewReader("command=fixture&roots="+url.QueryEscape("/a\nfile:///b")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	existing := config.ServerConfig{Command: "fixture", Roots: []string{"file:///old"}}
	got, err := buildServerConfig(parseServerForm(req), &existing)
	if err != nil || len(got.Roots) != 2 || got.Roots[0] != "file:///a" || got.Roots[1] != "file:///b" {
		t.Fatalf("roots = %v, %v", got.Roots, err)
	}
	if serverFormDataFromConfig("fixture", got).Roots != "file:///a\nfile:///b" {
		t.Error("edit form lost the roots")
	}

	absent := httptest.NewRequest("POST", "/servers", strings.NewReader("command=fixture"))
	absent.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = absent.ParseForm()
	if got, err := buildServerConfig(parseServerForm(absent), &existing); err != nil || len(got.Roots) != 1 {
		t.Fatalf("an absent roots field did not preserve them: %v, %v", got.Roots, err)
	}
}
