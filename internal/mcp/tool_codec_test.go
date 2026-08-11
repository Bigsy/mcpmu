package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// A tool definition must survive decode/encode byte-for-byte, including
// members no revision of the spec has defined yet. Real servers send fields
// mcpmu has never heard of, and a proxy that drops them is lying about the
// server it fronts.
func TestTool_RoundTripsUnknownFields(t *testing.T) {
	t.Parallel()
	const original = `{"name":"t","title":"T","description":"d",` +
		`"inputSchema":{"type":"object"},"outputSchema":{"type":"string"},` +
		`"annotations":{"readOnlyHint":true},"icons":[{"src":"x"}],` +
		`"_meta":{"a":1},"execution":{"taskSupport":"required"},` +
		`"futureField":{"nested":[1,2,3]},"anotherFuture":"scalar"}`

	var tool Tool
	if err := json.Unmarshal([]byte(original), &tool); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got := len(tool.Extra); got != 2 {
		t.Errorf("Extra holds %d members, want 2: %v", got, tool.Extra)
	}
	for _, key := range []string{"futureField", "anotherFuture"} {
		if _, ok := tool.Extra[key]; !ok {
			t.Errorf("unknown member %q was dropped", key)
		}
	}
	// Modelled fields must not be duplicated into the catch-all.
	for _, key := range []string{"name", "annotations", "execution", "_meta"} {
		if _, ok := tool.Extra[key]; ok {
			t.Errorf("modelled member %q leaked into Extra", key)
		}
	}

	encoded, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal([]byte(original), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Errorf("round trip changed the member count: %d → %d (%s)", len(before), len(after), encoded)
	}
	for key, want := range before {
		got, ok := after[key]
		if !ok {
			t.Errorf("member %q lost in the round trip", key)
			continue
		}
		if !sameJSON(string(want), string(got)) {
			t.Errorf("member %q changed: %s → %s", key, want, got)
		}
	}
}

// Extra must never be able to override a modelled field — otherwise a
// hand-built Tool could disagree with itself about its own name.
func TestTool_ModelledFieldsWinOverExtra(t *testing.T) {
	t.Parallel()
	tool := Tool{
		Name:  "real",
		Extra: map[string]json.RawMessage{"name": json.RawMessage(`"forged"`)},
	}
	encoded, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "forged") {
		t.Errorf("Extra overrode a modelled field: %s", encoded)
	}
}

func TestTool_MalformedInput(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{`"just a string"`, `[1,2,3]`, `{`, `null`} {
		var tool Tool
		err := json.Unmarshal([]byte(payload), &tool)
		if payload == `null` {
			// JSON null into a struct is a no-op, not an error.
			if err != nil {
				t.Errorf("unmarshal null: unexpected error %v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("unmarshal %s: expected an error, got tool %+v", payload, tool)
		}
	}
}

func TestParseToolAnnotations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		raw          string
		wantOK       bool
		wantReadOnly *bool
	}{
		{"absent", ``, false, nil},
		{"empty object", `{}`, true, nil},
		{"readOnly true", `{"readOnlyHint":true}`, true, new(true)},
		{"readOnly false", `{"readOnlyHint":false}`, true, new(false)},
		{"malformed", `{"readOnlyHint":`, false, nil},
		{"wrong shape", `[1,2,3]`, false, nil},
		{"wrong hint type", `{"readOnlyHint":"yes"}`, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var raw json.RawMessage
			if tt.raw != "" {
				raw = json.RawMessage(tt.raw)
			}
			got, ok := ParseToolAnnotations(raw)
			if ok != tt.wantOK {
				t.Fatalf("ParseToolAnnotations(%s) ok = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			switch {
			case tt.wantReadOnly == nil && got.ReadOnlyHint != nil:
				t.Errorf("readOnlyHint = %v, want absent", *got.ReadOnlyHint)
			case tt.wantReadOnly != nil && got.ReadOnlyHint == nil:
				t.Errorf("readOnlyHint absent, want %v", *tt.wantReadOnly)
			case tt.wantReadOnly != nil && *got.ReadOnlyHint != *tt.wantReadOnly:
				t.Errorf("readOnlyHint = %v, want %v", *got.ReadOnlyHint, *tt.wantReadOnly)
			}
		})
	}
}

func sameJSON(a, b string) bool {
	var left, right any
	if json.Unmarshal([]byte(a), &left) != nil || json.Unmarshal([]byte(b), &right) != nil {
		return false
	}
	leftEncoded, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightEncoded, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftEncoded) == string(rightEncoded)
}
