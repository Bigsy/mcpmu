package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestSharingSubmission(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"shared=false", false}, {"shared=false&shared=true", true}, {"", false},
	} {
		request := httptest.NewRequest("POST", "/servers", strings.NewReader("command=fixture&"+tc.body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		fd := parseServerForm(request)
		existing := config.ServerConfig{Command: "fixture", Shared: new(false), DeniedTools: []string{"danger"}}
		got, err := buildServerConfig(fd, &existing)
		if err != nil || got.IsShared() != tc.want || len(got.DeniedTools) != 1 {
			t.Fatalf("%s: %+v %v", tc.body, got, err)
		}
	}
	if !newServerFormData().Shared {
		t.Fatal("new form should default to shared")
	}
}
