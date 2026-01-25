# Phase 1.1: Testing Strategy & Fixtures

This document defines the testing infrastructure required to safely validate Phase 1–2 work. Without a fake MCP server and integration tests, it's difficult to verify the MCP client, process supervision, and TUI logic.

---

## Goals

1. **Fake MCP Server**: A minimal stdio MCP server for tests/CI that can simulate various scenarios
2. **Integration Tests**: Validate process lifecycle (start, handshake, list tools, stop, crash handling)
3. **Protocol Tests (Phase 1.5)**: Test mcp-studio's own stdio server mode end-to-end
4. **TUI Unit Tests**: Test Bubble Tea model logic without needing a terminal
5. **CI-Ready**: All tests run without external dependencies, Unix-only (macOS/Linux)

---

## Critical: Test Isolation

### Hermetic $HOME Requirement

**Tests must isolate `$HOME` to avoid mutating real user data.**

`process.NewSupervisor()` creates a `PIDTracker` that immediately runs orphan cleanup on construction (`internal/process/supervisor.go:41`). Both config loading and PID tracking use `os.UserHomeDir()` to locate `~/.config/mcp-studio/`.

Without isolation, tests can:
- Mutate real `~/.config/mcp-studio/config.json`
- Kill real "orphan" processes that happen to match

**Required test setup:**
```go
func setupTestHome(t *testing.T) string {
    t.Helper()
    tmpHome := t.TempDir()
    t.Setenv("HOME", tmpHome)
    // Also set for macOS
    t.Setenv("TMPDIR", tmpHome)
    return tmpHome
}
```

**Important:** Helper re-exec processes inherit environment, so `$HOME` override propagates automatically.

### Cross-Platform Limitations

The supervisor uses Unix signals (`SIGTERM`, `SIGKILL`, `syscall.Signal(0)`) which don't exist on Windows. **CI should target macOS and Linux only.** If Windows support is needed later, add build constraints and skip signal-based tests.

---

## Testing Pyramid

```
                    ┌─────────────────┐
                    │  Manual / E2E   │  ← Claude Code integration
                    │  (Phase 1.5+)   │
                    └────────┬────────┘
                             │
               ┌─────────────┴─────────────┐
               │      Protocol Tests       │  ← mcp-studio --stdio
               │      (Phase 1.5)          │     as MCP server
               └─────────────┬─────────────┘
                             │
          ┌──────────────────┴──────────────────┐
          │        Integration Tests            │  ← Fake MCP server
          │        (Phase 1)                    │     as subprocess
          └──────────────────┬──────────────────┘
                             │
    ┌────────────────────────┴────────────────────────┐
    │                  Unit Tests                      │  ← In-memory,
    │                  (All Phases)                    │     no I/O
    └──────────────────────────────────────────────────┘
```

**Phase 1.5 stdio mode becomes the primary integration test vehicle.** It validates:
- MCP protocol compliance (as a server)
- Tool aggregation and routing
- Config loading and server management
- Error handling end-to-end

---

## Fake MCP Server Architecture

### Design Principles

- **Library-first**: Implement as `internal/mcptest/fakeserver` with a pure `Serve(ctx, stdin, stdout, cfg)` function
- **Re-exec pattern**: Spawn the test binary itself as a subprocess (no separate build step, Unix)
- **Configurable behavior**: Pass config via environment variable for declarative test scenarios
- **Dual-use**: Also expose `cmd/mcp-fake-server` binary for manual debugging
- **NDJSON framing**: Use newline-delimited JSON (matching the actual MCP stdio implementation)

### Configuration Schema

```go
type FakeServerConfig struct {
    // Tools to return from tools/list
    Tools []Tool `json:"tools"`

    // Per-method delays (simulate slow responses)
    // NOTE: Use short delays (10-50ms) in tests to avoid slow suite.
    // The Supervisor retries with 500ms/1s/2s backoff, so "fail once then succeed"
    // scenarios should use FailOnAttempt, not long Delays.
    Delays map[string]time.Duration `json:"delays"`

    // Per-method forced errors (JSON-RPC error responses)
    Errors map[string]JSONRPCError `json:"errors"`

    // Crash behavior
    CrashOnMethod string `json:"crashOnMethod"`  // crash when this method is called
    CrashOnNthRequest int `json:"crashOnNthRequest"` // crash on Nth request (0 = never)
    CrashExitCode int `json:"crashExitCode"` // exit code when crashing

    // Retry testing: fail on specific attempt, succeed on others
    FailOnAttempt map[string]int `json:"failOnAttempt"` // method -> attempt number to fail (1-indexed)

    // Protocol edge cases for stream realism
    // The client loops to skip notifications and mismatched response IDs (client.go:169).
    // These options test that the client handles interleaved messages correctly.
    SendNotificationBeforeResponse bool `json:"sendNotificationBeforeResponse"` // send a notification before each response
    SendMismatchedIDFirst bool `json:"sendMismatchedIDFirst"` // send a response with wrong ID before correct one

    // Protocol edge cases
    Malformed bool `json:"malformed"` // write invalid JSON
}

type Tool struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    InputSchema any    `json:"inputSchema"`
}

type JSONRPCError struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
    Data    any    `json:"data,omitempty"`
}
```

### Test Helper Process Pattern

```go
// internal/mcptest/helper_test.go

func TestHelperProcess(t *testing.T) {
    if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
        return
    }

    cfgJSON := os.Getenv("FAKE_MCP_CFG")
    var cfg FakeServerConfig
    if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
        os.Exit(2)
    }

    if err := fakeserver.Serve(context.Background(), os.Stdin, os.Stdout, cfg); err != nil {
        os.Exit(1)
    }
    os.Exit(0)
}

func StartFakeServer(t *testing.T, cfg FakeServerConfig) (stdin io.WriteCloser, stdout io.ReadCloser, stop func()) {
    t.Helper()

    cfgJSON, _ := json.Marshal(cfg)
    cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
    cmd.Env = append(os.Environ(),
        "GO_WANT_HELPER_PROCESS=1",
        "FAKE_MCP_CFG="+string(cfgJSON),
    )

    var err error
    stdin, err = cmd.StdinPipe()
    if err != nil { t.Fatal(err) }
    stdout, err = cmd.StdoutPipe()
    if err != nil { t.Fatal(err) }
    stderr, _ := cmd.StderrPipe()

    if err := cmd.Start(); err != nil { t.Fatal(err) }
    go io.Copy(io.Discard, stderr) // prevent deadlock

    stop = func() {
        _ = stdin.Close() // graceful shutdown signal
        done := make(chan error, 1)
        go func() { done <- cmd.Wait() }()
        select {
        case <-time.After(2 * time.Second):
            _ = cmd.Process.Kill()
            <-done
        case <-done:
        }
    }
    t.Cleanup(stop)
    return
}
```

### Fake Server Implementation

```go
// internal/mcptest/fakeserver/serve.go

func Serve(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
    reader := bufio.NewReader(in)
    requestCount := 0
    methodAttempts := make(map[string]int) // track attempts per method for FailOnAttempt

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }

        // Read JSON-RPC request (NDJSON framing - read until newline)
        line, err := reader.ReadBytes('\n')
        if err == io.EOF {
            return nil
        }
        if err != nil {
            return err
        }

        var req rpcRequest
        if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
            return err
        }

        requestCount++
        methodAttempts[req.Method]++

        // Check crash conditions
        if cfg.CrashOnNthRequest > 0 && requestCount >= cfg.CrashOnNthRequest {
            os.Exit(cfg.CrashExitCode)
        }
        if cfg.CrashOnMethod != "" && req.Method == cfg.CrashOnMethod {
            os.Exit(cfg.CrashExitCode)
        }

        // Apply delay if configured
        if delay, ok := cfg.Delays[req.Method]; ok {
            time.Sleep(delay)
        }

        // Check for FailOnAttempt (for retry testing)
        if failAttempt, ok := cfg.FailOnAttempt[req.Method]; ok {
            if methodAttempts[req.Method] == failAttempt {
                writeErrorResponse(out, req.ID, JSONRPCError{
                    Code: -32603, Message: "Simulated failure on attempt",
                }, cfg)
                continue
            }
        }

        // Check for forced error
        if rpcErr, ok := cfg.Errors[req.Method]; ok {
            writeErrorResponse(out, req.ID, rpcErr, cfg)
            continue
        }

        // Handle methods
        switch req.Method {
        case "initialize":
            writeResponse(out, req.ID, InitializeResult{
                ProtocolVersion: "2024-11-05",
                ServerInfo: ServerInfo{Name: "fake-server", Version: "1.0.0"},
                Capabilities: Capabilities{Tools: &ToolsCapability{}},
            }, cfg)
        case "tools/list":
            writeResponse(out, req.ID, ToolsListResult{Tools: cfg.Tools}, cfg)
        case "notifications/initialized":
            // No response needed for notifications
        default:
            writeErrorResponse(out, req.ID, JSONRPCError{
                Code: -32601, Message: "Method not found",
            }, cfg)
        }
    }
}

// writeResponse writes a JSON-RPC response with optional stream noise
func writeResponse(out io.Writer, id int64, result any, cfg Config) {
    // Stream realism: send notification before response if configured
    if cfg.SendNotificationBeforeResponse {
        notification := rpcNotification{JSONRPC: "2.0", Method: "test/noise"}
        data, _ := json.Marshal(notification)
        out.Write(data)
        out.Write([]byte("\n"))
    }

    // Stream realism: send mismatched ID first if configured
    if cfg.SendMismatchedIDFirst {
        fakeResp := rpcResponse{JSONRPC: "2.0", ID: id + 9999, Result: json.RawMessage(`{}`)}
        data, _ := json.Marshal(fakeResp)
        out.Write(data)
        out.Write([]byte("\n"))
    }

    // Actual response (NDJSON)
    resp := rpcResponse{JSONRPC: "2.0", ID: id}
    resp.Result, _ = json.Marshal(result)
    data, _ := json.Marshal(resp)
    out.Write(data)
    out.Write([]byte("\n"))
}
```

---

## Integration Test Scenarios

### Test Matrix

| Scenario | Fake Server Config | Expected Behavior |
|----------|-------------------|-------------------|
| Happy path | Default tools | Initialize → list tools → stop cleanly |
| Notification before response | `sendNotificationBeforeResponse: true` | Client skips notification, gets correct response |
| Mismatched ID first | `sendMismatchedIDFirst: true` | Client skips wrong ID, waits for correct one |
| Fail once then succeed | `failOnAttempt: {"initialize": 1}` | Supervisor retry succeeds on 2nd attempt |
| Slow initialize | `delays: {"initialize": "50ms"}` + short timeout | Context timeout triggers, process cleaned up |
| Slow tools/list | `delays: {"tools/list": "50ms"}` + short timeout | Timeout after handshake, graceful degradation |
| Initialize error | `errors: {"initialize": {...}}` | Client surfaces error, no crash |
| Crash on initialize | `crashOnMethod: "initialize"` | Client detects EOF, marks server as crashed |
| Crash mid-session | `crashOnNthRequest: 3` | Client handles broken pipe gracefully |
| Malformed response | `malformed: true` | Client rejects, doesn't hang |
| Empty tool list | `tools: []` | Works correctly with zero tools |
| Many tools | 100+ tools | No performance issues |
| Concurrent stop | Multiple servers | `StopAll()` with `-race` detects no races |
| PID tracking consistency | Start/stop cycles | PIDs tracked and cleaned up correctly |

### Integration Test Structure

**Note:** The current client uses a two-step construction pattern:
```go
transport := mcp.NewStdioTransport(stdin, stdout)
client := mcp.NewClient(transport)
```

```go
// internal/mcp/integration_test.go
//go:build integration

func TestServerLifecycle_HappyPath(t *testing.T) {
    setupTestHome(t) // Isolate $HOME

    cfg := mcptest.FakeServerConfig{
        Tools: []mcptest.Tool{
            {Name: "read_file", Description: "Read a file"},
            {Name: "write_file", Description: "Write a file"},
        },
    }

    stdin, stdout, stop := mcptest.StartFakeServer(t, cfg)
    defer stop()

    transport := mcp.NewStdioTransport(stdin, stdout)
    client := mcp.NewClient(transport)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // Initialize
    err := client.Initialize(ctx)
    require.NoError(t, err)
    name, _ := client.ServerInfo()
    assert.Equal(t, "fake-server", name)

    // List tools
    tools, err := client.ListTools(ctx)
    require.NoError(t, err)
    assert.Len(t, tools, 2)
    assert.Equal(t, "read_file", tools[0].Name)

    // Stop
    err = client.Close()
    assert.NoError(t, err)
}

func TestServerLifecycle_NotificationBeforeResponse(t *testing.T) {
    setupTestHome(t)

    cfg := mcptest.FakeServerConfig{
        SendNotificationBeforeResponse: true,
        Tools: []mcptest.Tool{{Name: "test_tool"}},
    }

    stdin, stdout, stop := mcptest.StartFakeServer(t, cfg)
    defer stop()

    transport := mcp.NewStdioTransport(stdin, stdout)
    client := mcp.NewClient(transport)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // Client should skip the notification and get the real response
    err := client.Initialize(ctx)
    require.NoError(t, err)

    tools, err := client.ListTools(ctx)
    require.NoError(t, err)
    assert.Len(t, tools, 1)
}

func TestServerLifecycle_MismatchedIDFirst(t *testing.T) {
    setupTestHome(t)

    cfg := mcptest.FakeServerConfig{
        SendMismatchedIDFirst: true,
        Tools: []mcptest.Tool{{Name: "test_tool"}},
    }

    stdin, stdout, stop := mcptest.StartFakeServer(t, cfg)
    defer stop()

    transport := mcp.NewStdioTransport(stdin, stdout)
    client := mcp.NewClient(transport)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // Client should skip the mismatched ID and wait for correct one
    err := client.Initialize(ctx)
    require.NoError(t, err)
}

func TestServerLifecycle_CrashOnInitialize(t *testing.T) {
    setupTestHome(t)

    cfg := mcptest.FakeServerConfig{
        CrashOnMethod: "initialize",
        CrashExitCode: 1,
    }

    stdin, stdout, _ := mcptest.StartFakeServer(t, cfg)
    transport := mcp.NewStdioTransport(stdin, stdout)
    client := mcp.NewClient(transport)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    err := client.Initialize(ctx)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "EOF") // or whatever your error wrapping produces
}

func TestServerLifecycle_Timeout(t *testing.T) {
    setupTestHome(t)

    cfg := mcptest.FakeServerConfig{
        Delays: map[string]time.Duration{
            "initialize": 200 * time.Millisecond, // Short delay for fast tests
        },
    }

    stdin, stdout, _ := mcptest.StartFakeServer(t, cfg)
    transport := mcp.NewStdioTransport(stdin, stdout)
    client := mcp.NewClient(transport)

    // Very short timeout to trigger failure
    ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
    defer cancel()

    err := client.Initialize(ctx)
    require.Error(t, err)
    assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestConcurrentStopAll(t *testing.T) {
    setupTestHome(t)

    // This test should be run with -race to detect data races
    // in StopAll's concurrent goroutines and PID tracking

    // Start multiple fake servers, then stop them all concurrently
    // Verify no races in supervisor.handles map or pidTracker
}
```

---

## Protocol Tests (Phase 1.5)

These tests spawn `mcp-studio serve --stdio` and validate it as an MCP server.

### Test MCP Client for Protocol Tests

```go
// internal/testutil/mcpclient.go

// TestMCPClient speaks MCP protocol over stdin/stdout pipes
type TestMCPClient struct {
    stdin  io.WriteCloser
    stdout io.ReadCloser
    cmd    *exec.Cmd
}

func StartMCPStudio(t *testing.T, configPath string) *TestMCPClient {
    t.Helper()

    cmd := exec.Command("go", "run", "./cmd/mcp-studio", "serve", "--stdio", "--config", configPath)
    stdin, _ := cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    cmd.Stderr = os.Stderr // Show logs during test

    if err := cmd.Start(); err != nil {
        t.Fatal(err)
    }

    t.Cleanup(func() {
        stdin.Close()
        cmd.Wait()
    })

    return &TestMCPClient{stdin: stdin, stdout: stdout, cmd: cmd}
}

func (c *TestMCPClient) Initialize(ctx context.Context) (*InitializeResult, error) {
    // Send initialize request, read response
}

func (c *TestMCPClient) ListTools(ctx context.Context) ([]Tool, error) {
    // Send tools/list request, read response
}

func (c *TestMCPClient) CallTool(ctx context.Context, name string, args any) (*ToolResult, error) {
    // Send tools/call request, read response
}
```

### Protocol Test Scenarios

| Scenario | Config | Expected |
|----------|--------|----------|
| Empty config | No servers | Returns only manager tools |
| Single server | One fake server | Returns aggregated tools: `fake.tool1`, `fake.tool2`, `mcp-studio.*` |
| Multiple servers | Two fake servers | Returns tools from both with proper namespacing |
| Server not running | Server configured, not started | `tools/call` lazy-starts server |
| Server crash | Server crashes during call | Returns structured MCP error |
| Invalid tool | Call non-existent tool | Returns `-32601 Method not found` |
| Manager tools | Any config | `mcp-studio.servers_list` works |

### Protocol Test Implementation

```go
// internal/server/protocol_test.go
//go:build protocol

func TestProtocol_Initialize(t *testing.T) {
    configPath := createTestConfig(t, Config{
        Servers: []ServerConfig{},
    })

    client := testutil.StartMCPStudio(t, configPath)

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    result, err := client.Initialize(ctx)
    require.NoError(t, err)
    assert.Equal(t, "mcp-studio", result.ServerInfo.Name)
    assert.NotNil(t, result.Capabilities.Tools)
}

func TestProtocol_ToolsListWithManagedServer(t *testing.T) {
    // Start a fake MCP server first
    fakeServerPath := buildFakeServer(t)

    configPath := createTestConfig(t, Config{
        Servers: []ServerConfig{
            {ID: "fake", Name: "Fake", Command: fakeServerPath, Eager: true},
        },
    })

    client := testutil.StartMCPStudio(t, configPath)

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    _, err := client.Initialize(ctx)
    require.NoError(t, err)

    tools, err := client.ListTools(ctx)
    require.NoError(t, err)

    // Should have manager tools + fake server tools
    toolNames := extractToolNames(tools)
    assert.Contains(t, toolNames, "mcp-studio.servers_list")
    assert.Contains(t, toolNames, "fake.read_file")  // from fake server
}

func TestProtocol_ToolCallRouting(t *testing.T) {
    fakeServerPath := buildFakeServer(t)

    configPath := createTestConfig(t, Config{
        Servers: []ServerConfig{
            {ID: "fake", Name: "Fake", Command: fakeServerPath},
        },
    })

    client := testutil.StartMCPStudio(t, configPath)

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    _, err := client.Initialize(ctx)
    require.NoError(t, err)

    // Call a tool - should lazy-start the server
    result, err := client.CallTool(ctx, "fake.read_file", map[string]any{"path": "/tmp/test"})
    require.NoError(t, err)
    assert.NotNil(t, result)
}

func TestProtocol_ManagerTools(t *testing.T) {
    fakeServerPath := buildFakeServer(t)

    configPath := createTestConfig(t, Config{
        Servers: []ServerConfig{
            {ID: "fake", Name: "Fake", Command: fakeServerPath},
        },
    })

    client := testutil.StartMCPStudio(t, configPath)

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    _, err := client.Initialize(ctx)
    require.NoError(t, err)

    // List servers via manager tool
    result, err := client.CallTool(ctx, "mcp-studio.servers_list", nil)
    require.NoError(t, err)

    var servers []ServerStatus
    json.Unmarshal(result.Content, &servers)
    assert.Len(t, servers, 1)
    assert.Equal(t, "fake", servers[0].ID)
}
```

---

## TUI Unit Testing

### Testing Update Logic

Test the model as a pure state machine - no terminal needed:

```go
// internal/tui/model_test.go

func TestModel_TabSwitching(t *testing.T) {
    m := NewModel()
    assert.Equal(t, TabServers, m.activeTab)

    // Press '2' to switch to Namespaces
    newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
    m = newM.(Model)
    assert.Equal(t, TabNamespaces, m.activeTab)

    // Press '3' to switch to Proxies
    newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
    m = newM.(Model)
    assert.Equal(t, TabProxies, m.activeTab)
}

func TestModel_QuitKey(t *testing.T) {
    m := NewModel()

    _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
    require.NotNil(t, cmd)

    msg := cmd()
    _, isQuit := msg.(tea.QuitMsg)
    assert.True(t, isQuit)
}

func TestModel_FocusCycling(t *testing.T) {
    m := NewModel()
    assert.Equal(t, FocusLeft, m.focus)

    // Press Tab to cycle focus
    newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
    m = newM.(Model)
    assert.Equal(t, FocusRight, m.focus)

    // Press Tab again to cycle back
    newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
    m = newM.(Model)
    assert.Equal(t, FocusLeft, m.focus)
}
```

### Testing View Output

Avoid brittle full-screen assertions - test for key content presence:

```go
// internal/tui/view_test.go

func TestView_ContainsTabBar(t *testing.T) {
    m := NewModel()
    m.width = 120
    m.height = 40

    view := stripANSI(m.View())

    assert.Contains(t, view, "[1]Servers")
    assert.Contains(t, view, "[2]Namespaces")
    assert.Contains(t, view, "[3]Proxies")
}

func TestView_StatusBar(t *testing.T) {
    m := NewModel()
    m.width = 120
    m.height = 40
    m.serverCount = 3
    m.runningCount = 2

    view := stripANSI(m.View())

    assert.Contains(t, view, "2/3 servers running")
    assert.Contains(t, view, "?=help")
}

// Helper to strip ANSI escape codes
func stripANSI(s string) string {
    re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
    return re.ReplaceAllString(s, "")
}
```

### Testing Commands (Async Flows)

Execute commands synchronously in tests to simulate async flows:

```go
func TestModel_ServerStart_Command(t *testing.T) {
    m := NewModel()
    m.servers = []Server{{ID: "test", Name: "test-server", Status: StatusStopped}}
    m.selectedServer = 0

    // Press 's' to start server
    newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
    m = newM.(Model)

    require.NotNil(t, cmd)

    // Execute the command to get the resulting message
    msg := cmd()

    // Feed the message back into Update
    newM, _ = m.Update(msg)
    m = newM.(Model)

    // Assert server is now starting/running
    assert.Equal(t, StatusStarting, m.servers[0].Status)
}
```

---

## Test Organization

```
mcp-studio-go/
├── internal/
│   ├── mcptest/                    # Test infrastructure
│   │   ├── fakeserver/
│   │   │   ├── serve.go            # Fake MCP server implementation
│   │   │   ├── protocol.go         # JSON-RPC helpers
│   │   │   └── serve_test.go       # Unit tests for fake server itself
│   │   ├── helper.go               # StartFakeServer helper
│   │   ├── helper_test.go          # TestHelperProcess entry point
│   │   └── fixtures.go             # Common test configurations
│   │
│   ├── mcp/
│   │   ├── client.go               # MCP client implementation
│   │   ├── client_test.go          # Unit tests (in-memory pipes)
│   │   └── integration_test.go     # Integration tests (subprocess) [build tag]
│   │
│   ├── config/
│   │   ├── config.go
│   │   └── config_test.go          # Config parsing tests
│   │
│   ├── tui/
│   │   ├── model.go
│   │   ├── model_test.go           # Update logic tests
│   │   ├── view.go
│   │   ├── view_test.go            # View output tests
│   │   └── components/
│   │       ├── serverlist.go
│   │       └── serverlist_test.go
│   │
│   └── testutil/
│       ├── ansi.go                 # stripANSI helper
│       ├── timeout.go              # Test timeout helpers
│       └── process.go              # Process management helpers
│
├── cmd/
│   ├── mcp-studio-go/
│   │   └── main.go
│   └── mcp-fake-server/            # Optional standalone fake server
│       └── main.go
│
└── testdata/
    ├── configs/
    │   ├── valid.json
    │   ├── invalid.json
    │   └── empty.json
    └── tools/
        ├── obsidian-tools.json     # Sample tool definitions
        └── chrome-tools.json
```

---

## CI Configuration

### Build Tags

```go
//go:build integration

// Integration tests that spawn subprocesses
// Run with: go test -tags=integration ./...
```

### GitHub Actions Workflow

```yaml
name: Test

on: [push, pull_request]

jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - name: Unit Tests
        run: go test -race -v ./...

  integration:
    # Unix-only: uses SIGTERM/SIGKILL for process management
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - name: Integration Tests
        run: go test -tags=integration -race -v -timeout=5m ./...

  protocol:
    # Phase 1.5+: Test mcp-studio as MCP server
    # Unix-only: uses SIGTERM/SIGKILL for process management
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - name: Build mcp-studio
        run: go build -o mcp-studio ./cmd/mcp-studio
      - name: Protocol Tests
        run: go test -tags=protocol -race -v -timeout=5m ./...
```

### Test Timeouts

- Unit tests: 30 seconds default
- Integration tests: 5 minutes
- Individual subprocess tests: derive from `t.Deadline()` when available

### Race Detection

**Always run with `-race` flag.** The supervisor's `StopAll()` spawns concurrent goroutines (`supervisor.go:233-239`) and accesses shared state (`handles` map, `PIDTracker`). Race detection catches subtle bugs in concurrent start/stop scenarios.

---

## Implementation Tasks

### Test Infrastructure
- [ ] Create `internal/mcptest/fakeserver/` package
- [ ] Implement NDJSON framing (newline-delimited JSON)
- [ ] Implement `initialize` handler
- [ ] Implement `tools/list` handler
- [ ] Add delay injection support
- [ ] Add error injection support
- [ ] Add crash simulation support
- [ ] Add `FailOnAttempt` for retry testing
- [ ] Add `SendNotificationBeforeResponse` for stream realism
- [ ] Add `SendMismatchedIDFirst` for ID mismatch testing
- [ ] Create `StartFakeServer` test helper
- [ ] Create `TestHelperProcess` entry point
- [ ] Create `setupTestHome()` helper for $HOME isolation

### Test Fixtures
- [ ] Create `testdata/configs/` with valid/invalid configs
- [ ] Create `testdata/tools/` with sample tool definitions
- [ ] Create `internal/mcptest/fixtures.go` with common configs

### Integration Tests (Phase 1 - MCP Client)
- [ ] Happy path: initialize → list tools → stop
- [ ] Notification before response (stream realism)
- [ ] Mismatched ID before correct ID (stream realism)
- [ ] Fail once then succeed (retry testing)
- [ ] Timeout on initialize (short delays, not 3s+)
- [ ] Timeout on tools/list
- [ ] Server crash on initialize
- [ ] Server crash mid-session
- [ ] JSON-RPC error response
- [ ] Empty tool list
- [ ] Large tool list (100+)
- [ ] Concurrent StopAll with race detector
- [ ] PID tracking consistency across start/stop cycles

### Protocol Tests (Phase 1.5 - MCP Server)
- [ ] Create `internal/testutil/mcpclient.go` test helper
- [ ] Initialize handshake validates capabilities
- [ ] tools/list returns manager tools (empty config)
- [ ] tools/list aggregates from managed servers
- [ ] tools/call routes to correct server
- [ ] tools/call lazy-starts stopped server
- [ ] tools/call handles server failure gracefully
- [ ] Manager tool: servers_list
- [ ] Manager tool: servers_start
- [ ] Manager tool: servers_stop
- [ ] Invalid tool name returns proper error
- [ ] Timeout propagation works

### TUI Unit Tests
- [ ] Tab switching (1/2/3 keys)
- [ ] Focus cycling (Tab key)
- [ ] Quit key handling
- [ ] Server list navigation (j/k)
- [ ] View renders tab bar
- [ ] View renders status bar
- [ ] View renders server list items

### CI Setup
- [ ] Add `//go:build integration` tags
- [ ] Create `.github/workflows/test.yml`
- [ ] Add OS matrix for integration tests
- [ ] Configure race detector
- [ ] Set appropriate timeouts

---

## Dependencies on Other Phases

| Dependency | Required By | Notes |
|------------|-------------|-------|
| JSON-RPC framing | Phase 1 MCP client | Fake server validates client's wire format |
| Process supervision | Phase 1 server registry | Integration tests validate start/stop |
| Config schema | Phase 1 config | Test fixtures need valid config format |
| TUI model structure | Phase 2 TUI | Unit tests need stable model API |

---

## Success Criteria

1. `go test ./...` passes with only unit tests (fast, no subprocesses)
2. `go test -tags=integration -race ./...` passes on Linux and macOS (Unix-only due to signal handling)
3. Fake server can simulate all error conditions needed for robust client testing
4. Stream realism tests verify client handles notifications and mismatched IDs
5. TUI tests don't require a terminal (headless CI)
6. Test coverage > 70% for `internal/mcp/` and `internal/tui/`
7. No race conditions detected with `-race` in concurrent scenarios
