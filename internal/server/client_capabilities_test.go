package server

import (
	"encoding/json"
	"testing"
)

func TestParseClientCapabilities(t *testing.T) {
	t.Parallel()
	type check struct {
		feature, mode string
		want          bool
	}
	for _, tc := range []struct {
		name   string
		raw    string
		checks []check
	}{
		{name: "absent", raw: ``, checks: []check{
			{featureElicitation, "", false}, {featureSampling, "", false}, {featureRoots, "", false},
		}},
		{name: "empty elicitation means form only", raw: `{"elicitation":{}}`, checks: []check{
			{featureElicitation, "", true}, {featureElicitation, modeForm, true}, {featureElicitation, modeURL, false},
		}},
		{name: "url only", raw: `{"elicitation":{"url":{}}}`, checks: []check{
			{featureElicitation, modeForm, false}, {featureElicitation, modeURL, true},
		}},
		{name: "form and url", raw: `{"elicitation":{"form":{},"url":{}}}`, checks: []check{
			{featureElicitation, modeForm, true}, {featureElicitation, modeURL, true},
		}},
		{name: "sampling without tools", raw: `{"sampling":{}}`, checks: []check{
			{featureSampling, "", true}, {featureSampling, modeTools, false},
		}},
		{name: "sampling with tools", raw: `{"sampling":{"tools":{}}}`, checks: []check{
			{featureSampling, "", true}, {featureSampling, modeTools, true},
		}},
		{name: "roots", raw: `{"roots":{"listChanged":true}}`, checks: []check{
			{featureRoots, "", true}, {featureRoots, modeListChanged, true},
		}},
		{name: "malformed", raw: `{"elicitation":"yes"}`, checks: []check{
			{featureElicitation, "", false},
		}},
		{name: "unknown feature and mode", raw: `{"elicitation":{"form":{}}}`, checks: []check{
			{"telepathy", "", false}, {featureElicitation, "hologram", false},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := parseClientCapabilities(json.RawMessage(tc.raw))
			for _, c := range tc.checks {
				if got := caps.supports(c.feature, c.mode); got != c.want {
					t.Errorf("supports(%q, %q) = %v, want %v", c.feature, c.mode, got, c.want)
				}
			}
		})
	}
}

// TestSessionCapturesClientCapabilities: initialize records what the client
// declared, and nothing is supported before it.
func TestSessionCapturesClientCapabilities(t *testing.T) {
	t.Parallel()
	s, _ := newBareSession(t)
	if s.clientSupports(featureElicitation, "") {
		t.Fatal("elicitation supported before initialize")
	}
	respond(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"elicitation":{"form":{}},"roots":{}},"clientInfo":{"name":"t","version":"1"}}}`)
	if !s.clientSupports(featureElicitation, modeForm) || s.clientSupports(featureElicitation, modeURL) {
		t.Error("elicitation modes not captured")
	}
	if !s.clientSupports(featureRoots, "") || s.clientSupports(featureRoots, modeListChanged) {
		t.Error("roots not captured")
	}
	if s.clientSupports(featureSampling, "") {
		t.Error("sampling reported without being declared")
	}
}
