# Phase 1.1: Testing Strategy & Fixtures

This document defines the testing infrastructure required to safely validate Phase 1–2 work. Without a fake MCP server and integration tests, it's difficult to verify the MCP client, process supervision, and TUI logic.

---

## Goals

1. **Fake MCP Server**: A minimal stdio MCP server for tests/CI that can simulate various scenarios
2. **Integration Tests**: Validate process lifecycle (start, handshake, list tools, stop, crash handling)
3. **TUI Unit Tests**: Test Bubble Tea model logic without needing a terminal
4. **CI-Ready**: All tests run without external dependencies, cross-platform

---

## Fake MCP Server Architecture

### Design Principles

- **Library-first**: Implement as `internal/mcptest/fakeserver` with a pure `Serve(ctx, stdin, stdout, cfg)` function
- **Re-exec pattern**: Spawn the test binary itself as a subprocess (no separate build step, cross-platform)
- **Configurable behavior**: Pass config via environment variable for declarative test scenarios
- **Dual-use**: Also expose `cmd/mcp-fake-server` binary for manual debugging
- **Framing modes**: Support both Content-Length (default) and NDJSON to test client's dual-format reader

### Configuration Schema

```go
type FakeServerConfig struct {
    // Tools to return from tools/list
    Tools []Tool `json:"tools"`

    // Per-method delays (simulate slow responses)
    Delays map[string]time.Duration `json:"delays"`

    // Per-method forced errors (JSON-RPC error responses)
    Errors map[string]JSONRPCError `json:"errors"`

    // Crash behavior
    CrashOnMethod string `json:"crashOnMethod"`  // crash when this method is called
    CrashOnNthRequest int `json:"crashOnNthRequest"` // crash on Nth request (0 = never)
    CrashExitCode int `json:"crashExitCode"` // exit code when crashing

    // Wire format (to test client's dual-format reader)
    Framing string `json:"framing"` // "content-length" (default) or "ndjson"

    // Protocol edge cases
    Malformed bool `json:"malformed"` // write invalid JSON
    BadFraming bool `json:"badFraming"` // wrong Content-Length headers
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

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }

        // Read JSON-RPC request (Content-Length framing)
        req, err := readRequest(reader)
        if err == io.EOF {
            return nil
        }
        if err != nil {
            return err
        }

        requestCount++

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

        // Check for forced error
        if rpcErr, ok := cfg.Errors[req.Method]; ok {
            writeErrorResponse(out, req.ID, rpcErr)
            continue
        }

        // Handle methods
        switch req.Method {
        case "initialize":
            writeResponse(out, req.ID, InitializeResult{
                ProtocolVersion: "2024-11-05",
                ServerInfo: ServerInfo{Name: "fake-server", Version: "1.0.0"},
                Capabilities: Capabilities{Tools: &ToolsCapability{}},
            })
        case "tools/list":
            writeResponse(out, req.ID, ToolsListResult{Tools: cfg.Tools})
        case "notifications/initialized":
            // No response needed for notifications
        default:
            writeErrorResponse(out, req.ID, JSONRPCError{
                Code: -32601, Message: "Method not found",
            })
        }
    }
}
```

---

## Integration Test Scenarios

### Test Matrix

| Scenario | Fake Server Config | Expected Behavior |
|----------|-------------------|-------------------|
| Happy path | Default tools | Initialize → list tools → stop cleanly |
| Happy path (NDJSON) | `framing: "ndjson"` | Client reads NDJSON, still works |
| Slow initialize | `delays: {"initialize": "3s"}` | Context timeout triggers, process cleaned up |
| Slow tools/list | `delays: {"tools/list": "3s"}` | Timeout after handshake, graceful degradation |
| Initialize error | `errors: {"initialize": {...}}` | Client surfaces error, no crash |
| Crash on initialize | `crashOnMethod: "initialize"` | Client detects EOF, marks server as crashed |
| Crash mid-session | `crashOnNthRequest: 3` | Client handles broken pipe gracefully |
| Malformed response | `malformed: true` | Client rejects, doesn't hang |
| Bad framing | `badFraming: true` | Client handles gracefully (wrong Content-Length) |
| Empty tool list | `tools: []` | Works correctly with zero tools |
| Many tools | 100+ tools | No performance issues |

### Integration Test Structure

```go
// internal/mcp/integration_test.go
//go:build integration

func TestServerLifecycle_HappyPath(t *testing.T) {
    cfg := mcptest.FakeServerConfig{
        Tools: []mcptest.Tool{
            {Name: "read_file", Description: "Read a file"},
            {Name: "write_file", Description: "Write a file"},
        },
    }

    stdin, stdout, stop := mcptest.StartFakeServer(t, cfg)
    defer stop()

    client := mcp.NewStdioClient(stdin, stdout)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // Initialize
    info, err := client.Initialize(ctx)
    require.NoError(t, err)
    assert.Equal(t, "fake-server", info.ServerInfo.Name)

    // List tools
    tools, err := client.ListTools(ctx)
    require.NoError(t, err)
    assert.Len(t, tools, 2)
    assert.Equal(t, "read_file", tools[0].Name)

    // Stop
    err = client.Close()
    assert.NoError(t, err)
}

func TestServerLifecycle_CrashOnInitialize(t *testing.T) {
    cfg := mcptest.FakeServerConfig{
        CrashOnMethod: "initialize",
        CrashExitCode: 1,
    }

    stdin, stdout, _ := mcptest.StartFakeServer(t, cfg)
    client := mcp.NewStdioClient(stdin, stdout)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    _, err := client.Initialize(ctx)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "EOF") // or whatever your error wrapping produces
}

func TestServerLifecycle_Timeout(t *testing.T) {
    cfg := mcptest.FakeServerConfig{
        Delays: map[string]time.Duration{
            "initialize": 10 * time.Second,
        },
    }

    stdin, stdout, _ := mcptest.StartFakeServer(t, cfg)
    client := mcp.NewStdioClient(stdin, stdout)

    ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
    defer cancel()

    _, err := client.Initialize(ctx)
    require.Error(t, err)
    assert.ErrorIs(t, err, context.DeadlineExceeded)
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
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - name: Integration Tests
        run: go test -tags=integration -race -v -timeout=5m ./...
```

### Test Timeouts

- Unit tests: 30 seconds default
- Integration tests: 5 minutes (Windows process tests need slack)
- Individual subprocess tests: derive from `t.Deadline()` when available

---

## Implementation Tasks

### Test Infrastructure
- [ ] Create `internal/mcptest/fakeserver/` package
- [ ] Implement JSON-RPC framing (Content-Length headers)
- [ ] Implement `initialize` handler
- [ ] Implement `tools/list` handler
- [ ] Add delay injection support
- [ ] Add error injection support
- [ ] Add crash simulation support
- [ ] Create `StartFakeServer` test helper
- [ ] Create `TestHelperProcess` entry point

### Test Fixtures
- [ ] Create `testdata/configs/` with valid/invalid configs
- [ ] Create `testdata/tools/` with sample tool definitions
- [ ] Create `internal/mcptest/fixtures.go` with common configs

### Integration Tests
- [ ] Happy path: initialize → list tools → stop
- [ ] Timeout on initialize
- [ ] Timeout on tools/list
- [ ] Server crash on initialize
- [ ] Server crash mid-session
- [ ] JSON-RPC error response
- [ ] Empty tool list
- [ ] Large tool list (100+)

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
2. `go test -tags=integration ./...` passes on Linux, macOS, Windows
3. Fake server can simulate all error conditions needed for robust client testing
4. TUI tests don't require a terminal (headless CI)
5. Test coverage > 70% for `internal/mcp/` and `internal/tui/`
