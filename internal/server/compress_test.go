package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCompressionLevel(t *testing.T) {
	t.Parallel()
	valid := map[string]CompressionLevel{
		"":       CompressionOff,
		"off":    CompressionOff,
		"low":    CompressionLow,
		"medium": CompressionMedium,
		"HIGH":   CompressionHigh,
		"Max":    CompressionMax,
	}
	for in, want := range valid {
		got, err := ParseCompressionLevel(in)
		if err != nil {
			t.Errorf("ParseCompressionLevel(%q) error: %v", in, err)
		}
		if got != want {
			t.Errorf("ParseCompressionLevel(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"maximum", "on", "true", "1"} {
		if _, err := ParseCompressionLevel(in); err == nil {
			t.Errorf("ParseCompressionLevel(%q) should fail", in)
		}
	}
	if CompressionOff.enabled() {
		t.Error("off should not be enabled")
	}
	if !CompressionMedium.enabled() {
		t.Error("medium should be enabled")
	}
}

func TestSchemaArgNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		schema string
		want   []string
	}{
		{
			name:   "author order preserved, required first, optional suffixed",
			schema: `{"type":"object","properties":{"zebra":{"type":"string"},"apple":{"type":"string"},"mango":{"type":"integer"}},"required":["mango","zebra"]}`,
			want:   []string{"zebra", "mango", "apple?"},
		},
		{
			name:   "all optional",
			schema: `{"type":"object","properties":{"b":{},"a":{}}}`,
			want:   []string{"b?", "a?"},
		},
		{
			name:   "no properties",
			schema: `{"type":"object"}`,
			want:   nil,
		},
		{
			name:   "empty schema",
			schema: ``,
			want:   nil,
		},
		{
			name:   "non-object schema",
			schema: `["not","a","schema"]`,
			want:   nil,
		},
		{
			name:   "invalid JSON",
			schema: `{"properties":`,
			want:   nil,
		},
		{
			name:   "properties not an object",
			schema: `{"properties":"bogus"}`,
			want:   nil,
		},
		{
			name:   "required not an array",
			schema: `{"properties":{"a":{}},"required":"a"}`,
			want:   nil,
		},
		{
			name:   "nested properties do not leak",
			schema: `{"properties":{"outer":{"type":"object","properties":{"inner":{}}}},"required":["outer"]}`,
			want:   []string{"outer"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := schemaArgNames(json.RawMessage(tt.schema))
			if len(got) != len(tt.want) {
				t.Fatalf("schemaArgNames = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("schemaArgNames = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestFirstSentence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"period space", "Reads a file. Then more detail.", "Reads a file."},
		{"period end", "Reads a file.", "Reads a file."},
		{"no period", "Reads a file", "Reads a file"},
		{"dot in token", "Reads config.json from disk. More.", "Reads config.json from disk."},
		{"abbreviation stops it", "Use e.g. this tool. More.", "Use e.g."},
		{"empty", "", ""},
		{"period newline", "First.\nSecond.", "First."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstSentence(tt.in); got != tt.want {
				t.Errorf("firstSentence(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	long := strings.Repeat("x", 300) + ". done"
	got := firstSentence(long)
	if len([]rune(got)) != maxSentenceLen+1 { // 200 runes + ellipsis
		t.Errorf("capped sentence length = %d runes, want %d", len([]rune(got)), maxSentenceLen+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("capped sentence should end with ellipsis: %q", got)
	}
}

func listingFixture() []AggregatedTool {
	return []AggregatedTool{
		{
			Name:        "srv1.read_file",
			Description: "[srv1] Read a file from disk. Supports partial reads.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}},"required":["path"]}`),
		},
		{
			Name:        "srv2.get_time",
			Description: "[srv2]",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}
}

func TestFormatListing(t *testing.T) {
	t.Parallel()
	tools := listingFixture()

	tests := []struct {
		level CompressionLevel
		want  string
	}{
		{
			CompressionLow,
			"<tool>srv1.read_file(path, limit?): Read a file from disk. Supports partial reads.</tool>\n" +
				"<tool>srv2.get_time()</tool>",
		},
		{
			CompressionMedium,
			"<tool>srv1.read_file(path, limit?): Read a file from disk.</tool>\n" +
				"<tool>srv2.get_time()</tool>",
		},
		{
			CompressionHigh,
			"<tool>srv1.read_file(path, limit?)</tool>\n" +
				"<tool>srv2.get_time()</tool>",
		},
		{
			CompressionMax,
			"<tool>srv1.read_file</tool>\n" +
				"<tool>srv2.get_time</tool>",
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			if got := formatListing(tt.level, tools); got != tt.want {
				t.Errorf("formatListing(%s) =\n%s\nwant:\n%s", tt.level, got, tt.want)
			}
		})
	}

	if got := formatListing(CompressionMedium, nil); got != "" {
		t.Errorf("empty listing = %q, want empty string", got)
	}
}

func TestFormatListing_MultilineDescriptionFlattened(t *testing.T) {
	t.Parallel()
	tools := []AggregatedTool{{
		Name:        "srv.multi",
		Description: "[srv] Line one\nline   two continues here",
	}}
	got := formatListing(CompressionLow, tools)
	want := "<tool>srv.multi(): Line one line two continues here</tool>"
	if got != want {
		t.Errorf("formatListing = %q, want %q", got, want)
	}
}

func TestWrapperTools(t *testing.T) {
	t.Parallel()
	listing := "<tool>srv1.read_file(path)</tool>"
	wrappers := wrapperTools(listing)
	if len(wrappers) != 3 {
		t.Fatalf("wrapperTools returned %d tools, want 3", len(wrappers))
	}
	names := map[string]AggregatedTool{}
	for _, w := range wrappers {
		names[w.Name] = w
		// Wrapper names must never contain a dot — a dot would collide
		// with the {server}.{tool} namespace.
		if strings.Contains(w.Name, ".") {
			t.Errorf("wrapper name %q contains a dot", w.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(w.InputSchema, &schema); err != nil {
			t.Errorf("wrapper %q has invalid input schema: %v", w.Name, err)
		}
	}
	for _, want := range []string{wrapperListTools, wrapperGetToolSchema, wrapperInvokeTool} {
		if _, ok := names[want]; !ok {
			t.Errorf("wrapperTools missing %q", want)
		}
	}
	if !strings.Contains(names[wrapperInvokeTool].Description, listing) {
		t.Error("invoke_tool description does not embed the listing")
	}
}
