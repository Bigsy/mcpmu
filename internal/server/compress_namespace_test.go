package server

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

// nsCompressConfig builds a one-server config whose single namespace (auto-
// selected) carries the given compression level.
func nsCompressConfig(level string) *config.Config {
	return &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"read_file","description":"Read a file. Extra detail.","inputSchema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],"echoToolCalls":true}`),
		},
		Namespaces: map[string]config.NamespaceConfig{
			"work": {ServerIDs: []string{"srv1"}, Compression: level},
		},
	}
}

// TestCompress_NamespaceConfiguredLevel verifies a namespace-configured level
// compresses without the --compress flag.
func TestCompress_NamespaceConfiguredLevel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	script := initLine + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	responses := runCompressSession(t, Options{Config: nsCompressConfig("medium")}, script)

	tools := wrapperListFromResponse(t, responses[2])
	for _, want := range []string{wrapperListTools, wrapperGetToolSchema, wrapperInvokeTool} {
		if _, ok := tools[want]; !ok {
			t.Errorf("tools/list missing wrapper %q", want)
		}
	}
	if _, ok := tools["srv1.read_file"]; ok {
		t.Error("tools/list still exposes the real tool alongside the wrappers")
	}
	// The listing renders at the namespace's level (medium = first sentence).
	if desc := tools[wrapperInvokeTool].Description; !strings.Contains(desc, "<tool>srv1.read_file(path): Read a file.") {
		t.Errorf("invoke_tool description not rendered at medium level:\n%s", desc)
	}
}

// TestCompress_UnknownNamespaceLevelDegradesToOff pins the runtime fallback:
// an unknown level can reach a session (config load tolerates hand-edited and
// future-version values instead of bricking every command), and the session
// must serve the full uncompressed listing rather than fail.
func TestCompress_UnknownNamespaceLevelDegradesToOff(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	script := initLine + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	responses := runCompressSession(t, Options{Config: nsCompressConfig("ultra")}, script)

	tools := wrapperListFromResponse(t, responses[2])
	if _, ok := tools["srv1.read_file"]; !ok {
		t.Error("unknown level should degrade to off and list the real tool")
	}
	if _, ok := tools[wrapperInvokeTool]; ok {
		t.Error("unknown level should not enable the wrapper surface")
	}
}

// TestCompress_FlagOverridesNamespaceConfig verifies the --compress flag wins
// over namespace config in both directions: an explicit level replaces the
// configured one, and an explicit off (an explicit off override) disables the
// wrappers entirely.
func TestCompress_FlagOverridesNamespaceConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	script := initLine + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"

	// --compress off: full tools/list despite the namespace's medium.
	off := runCompressSession(t, Options{Config: nsCompressConfig("medium"), Compression: config.CompressionForce(config.CompressionOff)}, script)
	offTools := wrapperListFromResponse(t, off[2])
	if _, ok := offTools["srv1.read_file"]; !ok {
		t.Error("forced-off session should list the real tool")
	}
	if _, ok := offTools[wrapperInvokeTool]; ok {
		t.Error("forced-off session should not list wrapper tools")
	}

	// --compress high: wrappers render at high (args only), not medium.
	high := runCompressSession(t, Options{Config: nsCompressConfig("medium"), Compression: config.CompressionForce(config.CompressionHigh)}, script)
	highTools := wrapperListFromResponse(t, high[2])
	desc := highTools[wrapperInvokeTool].Description
	if !strings.Contains(desc, "<tool>srv1.read_file(path)</tool>") {
		t.Errorf("invoke_tool description not rendered at high level:\n%s", desc)
	}
	if strings.Contains(desc, "Read a file") {
		t.Errorf("high level should drop descriptions, got:\n%s", desc)
	}
}

// TestCompress_ReloadChangesNamespaceLevel verifies the effective level is
// resolved per request from the current config: a hot reload that flips the
// active namespace's compression takes effect on the next tools/list, in both
// directions, without restarting the session.
func TestCompress_ReloadChangesNamespaceLevel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdinReader.Close() }()
	defer func() { _ = stdinWriter.Close() }()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	defer func() { _ = stdoutReader.Close() }()
	defer func() { _ = stdoutWriter.Close() }()

	srv, err := New(Options{
		Config:        nsCompressConfig(""),
		PIDTrackerDir: t.TempDir(),
		Stdin:         stdinReader,
		Stdout:        stdoutWriter,
		ServerName:    "mcpmu-test",
		ServerVersion: "1.0.0",
		LogLevel:      "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	send := func(msg string) {
		t.Helper()
		if _, err := stdinWriter.WriteString(msg + "\n"); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	lines := make(chan string, 10)
	go func() {
		r := bufio.NewReader(stdoutReader)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				lines <- strings.TrimSpace(line)
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	readLine := func() string {
		t.Helper()
		select {
		case line := <-lines:
			return line
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for output line")
			return ""
		}
	}
	listTools := func(id int) map[string]AggregatedTool {
		t.Helper()
		send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/list"}`)
		return wrapperListFromResponse(t, json.RawMessage(readLine()))
	}

	send(initLine)
	_ = readLine()

	// Namespace compression off: the real tool is listed.
	if tools := listTools(2); tools["srv1.read_file"].Name == "" {
		t.Fatal("pre-reload tools/list should expose the real tool")
	}

	srv.applyReload(ctx, nsCompressConfig("medium"))
	_ = readLine() // notifications/tools/list_changed from the reload

	// Reload turned compression on: the next tools/list is compressed.
	tools := listTools(3)
	if _, ok := tools[wrapperInvokeTool]; !ok {
		t.Error("post-reload tools/list missing wrapper tools")
	}
	if _, ok := tools["srv1.read_file"]; ok {
		t.Error("post-reload tools/list still exposes the real tool")
	}

	srv.applyReload(ctx, nsCompressConfig(""))
	_ = readLine() // notifications/tools/list_changed

	// And back off again.
	tools = listTools(4)
	if _, ok := tools["srv1.read_file"]; !ok {
		t.Error("second reload should restore the real tool listing")
	}
	if _, ok := tools[wrapperInvokeTool]; ok {
		t.Error("second reload should drop the wrapper tools")
	}

	_ = stdinWriter.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not stop")
	}
}
