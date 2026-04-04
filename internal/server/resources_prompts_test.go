package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestServer_ResourcesList_FlagOff(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"resources/list"}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: false, // default
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d: %s", len(lines), stdout.String())
	}

	var resp struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("Expected MethodNotFound error when ExposeResources is false")
	}
	if resp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("Expected error code %d, got %d", ErrCodeMethodNotFound, resp.Error.Code)
	}
}

func TestServer_PromptsList_FlagOff(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"prompts/list"}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposePrompts:   false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("Expected MethodNotFound error when ExposePrompts is false")
	}
	if resp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("Expected error code %d, got %d", ErrCodeMethodNotFound, resp.Error.Code)
	}
}

func TestServer_ResourcesList_FlagOn_NoServers(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"resources/list"}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		ID     int `json:"id"`
		Result struct {
			Resources []json.RawMessage `json:"resources"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("Unexpected error: %v", resp.Error)
	}
	if len(resp.Result.Resources) != 0 {
		t.Errorf("Expected 0 resources, got %d", len(resp.Result.Resources))
	}
}

func TestServer_PromptsList_FlagOn_NoServers(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"prompts/list"}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposePrompts:   true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		ID     int `json:"id"`
		Result struct {
			Prompts []json.RawMessage `json:"prompts"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("Unexpected error: %v", resp.Error)
	}
	if len(resp.Result.Prompts) != 0 {
		t.Errorf("Expected 0 prompts, got %d", len(resp.Result.Prompts))
	}
}

func TestServer_ResourcesRead_FlagOff(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"srv:file:///test"}}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("Expected MethodNotFound error")
	}
	if resp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("Expected error code %d, got %d", ErrCodeMethodNotFound, resp.Error.Code)
	}
}

func TestServer_PromptsGet_FlagOff(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"srv.test"}}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposePrompts:   false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("Expected MethodNotFound error")
	}
	if resp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("Expected error code %d, got %d", ErrCodeMethodNotFound, resp.Error.Code)
	}
}

func TestServer_ResourcesRead_InvalidURI(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"no-colon"}}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("Expected InvalidParams error for URI without colon")
	}
	if resp.Error.Code != ErrCodeInvalidParams {
		t.Errorf("Expected error code %d, got %d", ErrCodeInvalidParams, resp.Error.Code)
	}
}

func TestServer_PromptsGet_InvalidName(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"no-dot"}}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposePrompts:   true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("Expected InvalidParams error for name without dot")
	}
	if resp.Error.Code != ErrCodeInvalidParams {
		t.Errorf("Expected error code %d, got %d", ErrCodeInvalidParams, resp.Error.Code)
	}
}

func TestServer_Initialize_CapabilitiesWithFlags(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: true,
		ExposePrompts:   true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	output := stdout.String()
	var resp struct {
		Result struct {
			Capabilities struct {
				Tools *struct {
					ListChanged bool `json:"listChanged"`
				} `json:"tools"`
				Resources *struct {
					ListChanged bool `json:"listChanged"`
				} `json:"resources"`
				Prompts *struct {
					ListChanged bool `json:"listChanged"`
				} `json:"prompts"`
			} `json:"capabilities"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("Unmarshal: %v\nOutput: %s", err, output)
	}
	if resp.Error != nil {
		t.Fatalf("Unexpected error: %v", resp.Error)
	}
	if resp.Result.Capabilities.Tools == nil {
		t.Fatal("Expected tools capability")
	}
	if resp.Result.Capabilities.Resources == nil {
		t.Fatal("Expected resources capability when ExposeResources is true")
	}
	if resp.Result.Capabilities.Prompts == nil {
		t.Fatal("Expected prompts capability when ExposePrompts is true")
	}
}

func TestServer_Initialize_CapabilitiesWithoutFlags(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: false,
		ExposePrompts:   false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	output := stdout.String()

	// Resources and prompts should NOT be in capabilities JSON at all
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw["result"], &result); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	var caps map[string]json.RawMessage
	if err := json.Unmarshal(result["capabilities"], &caps); err != nil {
		t.Fatalf("Unmarshal capabilities: %v", err)
	}

	if _, ok := caps["resources"]; ok {
		t.Error("Expected resources capability to be absent when ExposeResources is false")
	}
	if _, ok := caps["prompts"]; ok {
		t.Error("Expected prompts capability to be absent when ExposePrompts is false")
	}
}

// --- End-to-end tests with real upstream fake servers ---

func TestServer_ResourcesList_EndToEnd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[],"resources":[{"uri":"file:///readme.md","name":"readme","description":"The readme","mimeType":"text/markdown"},{"uri":"file:///config.json","name":"config","description":"App config"}],"resourceContents":{"file:///readme.md":[{"uri":"file:///readme.md","text":"# Hello World"}],"file:///config.json":[{"uri":"file:///config.json","text":"{\"key\":\"value\"}"}]}}`,
				},
			},
			"srv2": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[],"resources":[{"uri":"exa://tools/list","name":"tools","description":"List of tools"}],"resourceContents":{"exa://tools/list":[{"uri":"exa://tools/list","text":"tool1,tool2"}]}}`,
				},
			},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"resources/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"srv1:file:///readme.md"}}` + "\n" +
			`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"srv2:exa://tools/list"}}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 4 {
		t.Fatalf("Expected 4 responses, got %d:\n%s", len(lines), stdout.String())
	}

	// Response 2: resources/list — should have qualified URIs from both servers
	var listResp struct {
		ID     int `json:"id"`
		Result struct {
			Resources []struct {
				URI         string `json:"uri"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"resources"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("Unmarshal resources/list: %v\nLine: %s", err, lines[1])
	}
	if listResp.Error != nil {
		t.Fatalf("resources/list error: %v", listResp.Error)
	}

	// Should have 3 resources total (2 from srv1, 1 from srv2)
	if len(listResp.Result.Resources) != 3 {
		t.Fatalf("Expected 3 resources, got %d: %+v", len(listResp.Result.Resources), listResp.Result.Resources)
	}

	// Check URIs are qualified
	uris := make(map[string]bool)
	for _, r := range listResp.Result.Resources {
		uris[r.URI] = true
	}
	for _, expected := range []string{"srv1:file:///readme.md", "srv1:file:///config.json", "srv2:exa://tools/list"} {
		if !uris[expected] {
			t.Errorf("Expected qualified URI %q in resources list, got: %v", expected, uris)
		}
	}

	// Response 3: resources/read srv1:file:///readme.md — should strip prefix and return content
	var readResp1 struct {
		ID     int `json:"id"`
		Result struct {
			Contents json.RawMessage `json:"contents"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &readResp1); err != nil {
		t.Fatalf("Unmarshal resources/read[1]: %v\nLine: %s", err, lines[2])
	}
	if readResp1.Error != nil {
		t.Fatalf("resources/read[1] error: %v", readResp1.Error)
	}
	if !strings.Contains(string(readResp1.Result.Contents), "Hello World") {
		t.Errorf("Expected contents to contain 'Hello World', got: %s", string(readResp1.Result.Contents))
	}

	// Response 4: resources/read srv2:exa://tools/list — prefix stripping with multi-colon URI
	var readResp2 struct {
		ID     int `json:"id"`
		Result struct {
			Contents json.RawMessage `json:"contents"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[3]), &readResp2); err != nil {
		t.Fatalf("Unmarshal resources/read[2]: %v\nLine: %s", err, lines[3])
	}
	if readResp2.Error != nil {
		t.Fatalf("resources/read[2] error: %v", readResp2.Error)
	}
	if !strings.Contains(string(readResp2.Result.Contents), "tool1,tool2") {
		t.Errorf("Expected contents to contain 'tool1,tool2', got: %s", string(readResp2.Result.Contents))
	}
}

func TestServer_PromptsList_EndToEnd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[],"prompts":[{"name":"summarize","description":"Summarize text","arguments":[{"name":"text","required":true}]},{"name":"translate","description":"Translate text"}],"promptMessages":{"summarize":[{"role":"user","content":{"type":"text","text":"Summarize this"}}],"translate":[{"role":"user","content":{"type":"text","text":"Translate this"}}]}}`,
				},
			},
			"srv2": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[],"prompts":[{"name":"greet","description":"Generate greeting"}],"promptMessages":{"greet":[{"role":"user","content":{"type":"text","text":"Hello!"}}]}}`,
				},
			},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"prompts/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"prompts/get","params":{"name":"srv1.summarize","arguments":{"text":"hello"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":4,"method":"prompts/get","params":{"name":"srv2.greet"}}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposePrompts:   true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 4 {
		t.Fatalf("Expected 4 responses, got %d:\n%s", len(lines), stdout.String())
	}

	// Response 2: prompts/list — qualified names from both servers
	var listResp struct {
		ID     int `json:"id"`
		Result struct {
			Prompts []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"prompts"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("Unmarshal prompts/list: %v\nLine: %s", err, lines[1])
	}
	if listResp.Error != nil {
		t.Fatalf("prompts/list error: %v", listResp.Error)
	}

	// Should have 3 prompts total (2 from srv1, 1 from srv2)
	if len(listResp.Result.Prompts) != 3 {
		t.Fatalf("Expected 3 prompts, got %d: %+v", len(listResp.Result.Prompts), listResp.Result.Prompts)
	}

	// Check names are qualified and descriptions prefixed
	names := make(map[string]string)
	for _, p := range listResp.Result.Prompts {
		names[p.Name] = p.Description
	}
	for _, expected := range []string{"srv1.summarize", "srv1.translate", "srv2.greet"} {
		desc, ok := names[expected]
		if !ok {
			t.Errorf("Expected qualified prompt name %q, got: %v", expected, names)
			continue
		}
		// Description should be prefixed with [serverName]
		serverName := strings.SplitN(expected, ".", 2)[0]
		if !strings.HasPrefix(desc, "["+serverName+"]") {
			t.Errorf("Expected description to start with [%s], got: %q", serverName, desc)
		}
	}

	// Response 3: prompts/get srv1.summarize — should strip prefix and route correctly
	var getResp1 struct {
		ID     int `json:"id"`
		Result struct {
			Messages json.RawMessage `json:"messages"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &getResp1); err != nil {
		t.Fatalf("Unmarshal prompts/get[1]: %v\nLine: %s", err, lines[2])
	}
	if getResp1.Error != nil {
		t.Fatalf("prompts/get[1] error: %v", getResp1.Error)
	}
	if !strings.Contains(string(getResp1.Result.Messages), "Summarize this") {
		t.Errorf("Expected messages to contain 'Summarize this', got: %s", string(getResp1.Result.Messages))
	}

	// Response 4: prompts/get srv2.greet — should route to srv2
	var getResp2 struct {
		ID     int `json:"id"`
		Result struct {
			Messages json.RawMessage `json:"messages"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[3]), &getResp2); err != nil {
		t.Fatalf("Unmarshal prompts/get[2]: %v\nLine: %s", err, lines[3])
	}
	if getResp2.Error != nil {
		t.Fatalf("prompts/get[2] error: %v", getResp2.Error)
	}
	if !strings.Contains(string(getResp2.Result.Messages), "Hello!") {
		t.Errorf("Expected messages to contain 'Hello!', got: %s", string(getResp2.Result.Messages))
	}
}

func TestServer_ResourcesList_PartialFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"good": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[],"resources":[{"uri":"file:///good.txt","name":"good","description":"Good resource"}]}`,
				},
			},
			"bad": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[],"errors":{"resources/list":{"code":-32603,"message":"Internal error"}}}`,
				},
			},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"resources/list"}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d:\n%s", len(lines), stdout.String())
	}

	var listResp struct {
		ID     int `json:"id"`
		Result struct {
			Resources []struct {
				URI string `json:"uri"`
			} `json:"resources"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("Unmarshal: %v\nLine: %s", err, lines[1])
	}
	if listResp.Error != nil {
		t.Fatalf("Expected success with partial results, got error: %v", listResp.Error)
	}

	// Should have 1 resource from "good", "bad" server's error should be skipped
	if len(listResp.Result.Resources) != 1 {
		t.Fatalf("Expected 1 resource (partial result), got %d", len(listResp.Result.Resources))
	}
	if listResp.Result.Resources[0].URI != "good:file:///good.txt" {
		t.Errorf("Expected URI 'good:file:///good.txt', got %q", listResp.Result.Resources[0].URI)
	}
}

func TestServer_PromptsList_PartialFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"good": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[],"prompts":[{"name":"hello","description":"Say hello"}]}`,
				},
			},
			"bad": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[],"errors":{"prompts/list":{"code":-32603,"message":"Internal error"}}}`,
				},
			},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"prompts/list"}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposePrompts:   true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d:\n%s", len(lines), stdout.String())
	}

	var listResp struct {
		ID     int `json:"id"`
		Result struct {
			Prompts []struct {
				Name string `json:"name"`
			} `json:"prompts"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("Unmarshal: %v\nLine: %s", err, lines[1])
	}
	if listResp.Error != nil {
		t.Fatalf("Expected success with partial results, got error: %v", listResp.Error)
	}

	// Should have 1 prompt from "good", "bad" server's error should be skipped
	if len(listResp.Result.Prompts) != 1 {
		t.Fatalf("Expected 1 prompt (partial result), got %d", len(listResp.Result.Prompts))
	}
	if listResp.Result.Prompts[0].Name != "good.hello" {
		t.Errorf("Expected prompt name 'good.hello', got %q", listResp.Result.Prompts[0].Name)
	}
}

func TestServer_ResourcesRead_ServerNotInNamespace(t *testing.T) {
	t.Parallel()
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Kind: config.ServerKindStdio, Enabled: &enabled, Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"ns1": {ServerIDs: []string{"srv1"}},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"unknown:file:///test"}}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Namespace:       "ns1",
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("Expected error for server not in namespace")
	}
	if resp.Error.Code != ErrCodeServerNotFound {
		t.Errorf("Expected error code %d (ServerNotFound), got %d", ErrCodeServerNotFound, resp.Error.Code)
	}
}

func TestServer_PromptsGet_ServerNotInNamespace(t *testing.T) {
	t.Parallel()
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Kind: config.ServerKindStdio, Enabled: &enabled, Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"ns1": {ServerIDs: []string{"srv1"}},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"unknown.test"}}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Namespace:       "ns1",
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposePrompts:   true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("Expected error for server not in namespace")
	}
	if resp.Error.Code != ErrCodeServerNotFound {
		t.Errorf("Expected error code %d (ServerNotFound), got %d", ErrCodeServerNotFound, resp.Error.Code)
	}
}
