package mcptest

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Bigsy/mcpmu/internal/mcptest/fakeserver"
)

// TestHelperProcess is the entry point for the fake server subprocess.
// This is invoked by StartFakeServer via exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--").
// It runs the fake server when GO_WANT_HELPER_PROCESS=1 is set.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	cfgJSON := os.Getenv("FAKE_MCP_CFG")
	if cfgJSON == "" {
		os.Exit(2)
	}

	var cfg fakeserver.Config
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		os.Exit(2)
	}

	if err := fakeserver.Serve(context.Background(), os.Stdin, os.Stdout, cfg); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
