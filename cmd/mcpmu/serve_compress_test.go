package main

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/server"
)

// TestCompressionWireValue pins the tri-state handshake encoding: "" = flag
// absent (namespace config decides), "off" = explicit off, otherwise the
// level. The daemon decodes force-off as a non-empty string that parses to
// CompressionOff.
func TestCompressionWireValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		level    server.CompressionLevel
		forceOff bool
		want     string
	}{
		{server.CompressionOff, false, ""},
		{server.CompressionOff, true, "off"},
		{server.CompressionMedium, false, "medium"},
		{server.CompressionMax, false, "max"},
	}
	for _, tt := range tests {
		if got := compressionWireValue(tt.level, tt.forceOff); got != tt.want {
			t.Errorf("compressionWireValue(%q, %v) = %q, want %q", tt.level, tt.forceOff, got, tt.want)
		}
	}
}
