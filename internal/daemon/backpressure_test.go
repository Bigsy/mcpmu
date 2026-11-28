//go:build !windows

package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"
)

// openInitializedSession handshakes a daemon session and completes the MCP
// initialize exchange.
func openInitializedSession(t *testing.T, d runningTestDaemon) (*net.UnixConn, *bufio.Reader) {
	t.Helper()
	conn, reader, response := dialHandshake(t, d, Handshake{Type: "session", Protocol: SessionProtocol, Build: d.build})
	if !response.OK {
		t.Fatalf("handshake rejected: %+v", response)
	}
	t.Cleanup(func() { _ = conn.Close() })
	rpcCall(t, conn, reader, `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1"}}}`)
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	return conn, reader
}

// floodPings sends n pings from a goroutine without reading any response.
// Their responses far exceed the kernel's socket buffers.
func floodPings(conn net.Conn, n int) {
	go func() {
		w := bufio.NewWriter(conn)
		for i := range n {
			if _, err := fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"method":"ping"}`+"\n", i); err != nil {
				return
			}
		}
		_ = w.Flush()
	}()
}

// A client that pauses reading while a burst of responses arrives keeps its
// session and receives every response once it reads again.
func TestDaemonSessionSurvivesResponseBurst(t *testing.T) {
	d := startTestDaemon(t, nil)
	conn, reader := openInitializedSession(t, d)
	const pings = 20000
	floodPings(conn, pings)
	time.Sleep(500 * time.Millisecond)

	for want := 0; want < pings; {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("after %d of %d responses: %v", want, pings, err)
		}
		var msg struct {
			ID *int `json:"id"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("response %d: %v: %s", want, err, line)
		}
		if msg.ID == nil {
			continue // a notification
		}
		if *msg.ID != want {
			t.Fatalf("response id = %d, want %d", *msg.ID, want)
		}
		want++
	}
}

// One client that stops reading must not delay config reloads for the rest.
func TestDaemonStalledSessionDoesNotBlockReload(t *testing.T) {
	d := startTestDaemon(t, nil)
	_, healthy := openInitializedSession(t, d)
	changed := make(chan struct{}, 16)
	go func() {
		for {
			line, err := healthy.ReadBytes('\n')
			if err != nil {
				return
			}
			if bytes.Contains(line, []byte("notifications/tools/list_changed")) {
				changed <- struct{}{}
			}
		}
	}()
	stalled, _ := openInitializedSession(t, d)
	floodPings(stalled, 20000)
	time.Sleep(500 * time.Millisecond)

	for n := range 2 {
		for len(changed) > 0 {
			<-changed
		}
		config := fmt.Sprintf(`{"schemaVersion":1,"servers":{},"namespaces":{},"interaction_timeout_sec":%d}`, 601+n)
		if err := os.WriteFile(d.configPath, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		select {
		case <-changed:
		case <-time.After(5 * time.Second):
			t.Fatalf("reload %d: healthy session got no tools/list_changed while another client stalled", n+1)
		}
	}
}

// A client whose socket accepts nothing for the stall timeout is dropped;
// an idle client that keeps reading is not.
func TestDaemonDropsStalledSession(t *testing.T) {
	const stall = 200 * time.Millisecond
	d := startTestDaemon(t, func(opts *Options) { opts.WriteStallTimeout = stall })
	idleConn, idle := openInitializedSession(t, d)
	stalled, _ := openInitializedSession(t, d)
	floodPings(stalled, 20000)

	deadline := time.Now().Add(5 * time.Second)
	for {
		status, _, err := Inspect(t.Context(), d.configPath)
		if err == nil && status.Sessions == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stalled session was not dropped: sessions=%d err=%v", status.Sessions, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The idle session sat well past the stall timeout and still answers.
	time.Sleep(2 * stall)
	if response := rpcCall(t, idleConn, idle, `{"jsonrpc":"2.0","id":"after","method":"ping"}`); response["id"] != "after" {
		t.Fatalf("idle session ping: %v", response)
	}
}
