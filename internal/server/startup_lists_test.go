package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

// A stalled optional catalog must not delay the compressed tool surface or
// prevent the client from cancelling that catalog request.
func TestStartupListsDoNotBlockToolsOrCancellation(t *testing.T) {
	for _, method := range []string{"resources/list", "prompts/list"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			requestLog := filepath.Join(t.TempDir(), "requests.log")
			fake, err := json.Marshal(map[string]any{
				"tools":              []map[string]string{{"name": "ready"}},
				"advertiseResources": true, "advertisePrompts": true,
				"delays":         map[string]time.Duration{method: 30 * time.Second},
				"requestLogPath": requestLog,
			})
			if err != nil {
				t.Fatal(err)
			}
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			output := &lockedBuffer{}
			srv, err := New(Options{
				SessionOptions: SessionOptions{
					ExposeResources: true, ExposePrompts: true,
					Compression: config.CompressionForce(config.CompressionMedium),
				},
				Config: &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
					"upstream": {
						Command: os.Args[0], Args: []string{"-test.run=TestHelperProcess", "--"},
						Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_MCP_CFG": string(fake)},
					},
				}},
				PIDTrackerDir: t.TempDir(), Stdin: input, Stdout: output,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- srv.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				_ = writer.Close()
				_ = input.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("session did not shut down")
				}
			})
			write := func(line string) {
				t.Helper()
				if _, err := fmt.Fprintln(writer, line); err != nil {
					t.Fatal(err)
				}
			}
			wait := func(description string, condition func() bool) {
				t.Helper()
				deadline := time.Now().Add(3 * time.Second)
				for !condition() {
					if time.Now().After(deadline) {
						t.Fatalf("timed out waiting for %s; output: %s", description, output.String())
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			response := func(id string) bool {
				_, ok := responsesByID(t, output.String())[id]
				return ok
			}
			write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"test","version":"1"}}}`)
			write(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
			wait("initial tool discovery", func() bool { return response("2") })
			write(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":%q}`, method))
			wait("upstream list request", func() bool {
				data, _ := os.ReadFile(requestLog)
				return strings.Contains(string(data), method)
			})
			write(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
			write(`{"jsonrpc":"2.0","id":5,"method":"ping"}`)
			wait("tools/list and ping while upstream is stalled", func() bool { return response("4") && response("5") })
			resp := responsesByID(t, output.String())["4"]
			if resp.Error != nil || !strings.Contains(string(resp.Result), "upstream.ready") {
				t.Fatalf("compressed tool listing lost ready tool: %+v", resp)
			}
			if response("3") {
				t.Fatal("stalled list completed before cancellation")
			}
			write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`)
			wait("cancelled list handler to finish", func() bool { return response("3") })
		})
	}
}
