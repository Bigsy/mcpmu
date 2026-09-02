package config

import (
	"encoding/json"
	"testing"
)

// TestCompressionOverride_TextRoundTrip pins the tri-state wire encoding that
// the daemon handshake carries: "" = flag absent (the active namespace's
// configured level decides), "off" = explicit off, otherwise the level.
func TestCompressionOverride_TextRoundTrip(t *testing.T) {
	tests := []struct {
		override CompressionOverride
		want     string
	}{
		{CompressionUnset(), ""},
		{CompressionForce(CompressionOff), "off"},
		{CompressionForce(CompressionMedium), "medium"},
		{CompressionForce(CompressionMax), "max"},
	}
	for _, tt := range tests {
		text, err := tt.override.MarshalText()
		if err != nil {
			t.Fatalf("MarshalText(%+v): %v", tt.override, err)
		}
		if string(text) != tt.want {
			t.Errorf("MarshalText(%+v) = %q, want %q", tt.override, text, tt.want)
		}
		var back CompressionOverride
		if err := back.UnmarshalText(text); err != nil {
			t.Fatalf("UnmarshalText(%q): %v", text, err)
		}
		if back != tt.override {
			t.Errorf("round trip of %q = %+v, want %+v", text, back, tt.override)
		}
	}
}

func TestCompressionOverride_UnmarshalTextRejectsBadLevel(t *testing.T) {
	for _, in := range []string{"bogus", "on", "true", "maximum"} {
		var o CompressionOverride
		if err := o.UnmarshalText([]byte(in)); err == nil {
			t.Errorf("UnmarshalText(%q) = nil, want error", in)
		}
	}
}

// TestCompressionOverride_JSONUnsetForms pins that an absent key (an older
// shim that never sent the field) and an explicit empty string both decode to
// unset, so the daemon treats them identically.
func TestCompressionOverride_JSONUnsetForms(t *testing.T) {
	type payload struct {
		Compression CompressionOverride `json:"compression"`
	}
	for _, in := range []string{`{}`, `{"compression":""}`, `{"compression":"  "}`} {
		var p payload
		if err := json.Unmarshal([]byte(in), &p); err != nil {
			t.Fatalf("Unmarshal(%s): %v", in, err)
		}
		if p.Compression.Set() {
			t.Errorf("Unmarshal(%s) = %+v, want unset", in, p.Compression)
		}
	}
	var p payload
	if err := json.Unmarshal([]byte(`{"compression":"bogus"}`), &p); err == nil {
		t.Error("Unmarshal accepted an invalid compression level")
	}
}

func TestCompressionOverride_Resolve(t *testing.T) {
	tests := []struct {
		override   CompressionOverride
		configured string
		want       CompressionLevel
	}{
		// No flag: the namespace's configured level decides.
		{CompressionUnset(), "", CompressionOff},
		{CompressionUnset(), "medium", CompressionMedium},
		// A hand-edited bad value degrades to off rather than erroring.
		{CompressionUnset(), "bogus", CompressionOff},
		// An explicit flag wins in both directions.
		{CompressionForce(CompressionOff), "medium", CompressionOff},
		{CompressionForce(CompressionHigh), "medium", CompressionHigh},
		{CompressionForce(CompressionHigh), "", CompressionHigh},
	}
	for _, tt := range tests {
		if got := tt.override.Resolve(tt.configured); got != tt.want {
			t.Errorf("%+v.Resolve(%q) = %q, want %q", tt.override, tt.configured, got, tt.want)
		}
	}
}

func TestParseCompressionOverride(t *testing.T) {
	tests := []struct {
		in    string
		given bool
		want  CompressionOverride
	}{
		{"", false, CompressionUnset()},
		{"", true, CompressionForce(CompressionOff)},
		{"off", true, CompressionForce(CompressionOff)},
		{"medium", true, CompressionForce(CompressionMedium)},
		{" Medium ", true, CompressionForce(CompressionMedium)},
		// A value without the flag being given cannot happen via cobra, but
		// the override must still ignore it rather than forcing a level.
		{"medium", false, CompressionUnset()},
	}
	for _, tt := range tests {
		got, err := ParseCompressionOverride(tt.in, tt.given)
		if err != nil {
			t.Fatalf("ParseCompressionOverride(%q, %v): %v", tt.in, tt.given, err)
		}
		if got != tt.want {
			t.Errorf("ParseCompressionOverride(%q, %v) = %+v, want %+v", tt.in, tt.given, got, tt.want)
		}
	}
	if _, err := ParseCompressionOverride("bogus", true); err == nil {
		t.Error("ParseCompressionOverride accepted an invalid level")
	}
}
