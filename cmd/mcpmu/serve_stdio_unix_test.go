//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/server"
	"golang.org/x/sys/unix"
)

func TestEmbeddedSharedStdioSocket(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	stdin := os.NewFile(uintptr(fds[0]), "stdin")
	defer func() { _ = stdin.Close() }()
	out, err := unix.Dup(fds[0])
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.NewFile(uintptr(out), "stdout")
	defer func() { _ = stdout.Close() }()
	peerFile := os.NewFile(uintptr(fds[1]), "client")
	defer func() { _ = peerFile.Close() }()
	peer, err := net.FileConn(peerFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	if err := peer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	input := &observedInput{ReadCloser: stdin, started: started}
	done := make(chan error, 1)
	go func() {
		done <- serveEmbeddedSession(t.Context(), input, server.Options{Config: &config.Config{}, Stdout: stdout}, time.Second)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not start reading stdin")
	}
	flags, err := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_NONBLOCK != 0 {
		t.Fatalf("inherited socket flags = %#x, error = %v", flags, err)
	}
	if _, err := fmt.Fprintln(peer, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`); err != nil {
		t.Fatal(err)
	}
	if err := peer.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(peer).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response server.RPCResponse
	if err := json.Unmarshal(line, &response); err != nil || string(response.ID) != "1" || response.Error != nil || len(response.Result) == 0 {
		t.Fatalf("initialize response = %s, error = %v", line, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit after input EOF")
	}
}

type observedInput struct {
	io.ReadCloser
	started chan struct{}
}

func (in *observedInput) Read(p []byte) (int, error) {
	if in.started != nil {
		close(in.started)
		in.started = nil
	}
	return in.ReadCloser.Read(p)
}
