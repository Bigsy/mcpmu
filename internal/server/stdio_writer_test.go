package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
)

func TestStdioWritesCompleteFrames(t *testing.T) {
	var w shortFrameWriter
	s := &Session{writer: &w}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() { s.send(map[string]int{"id": i}) })
	}
	wg.Wait()

	seen := map[int]bool{}
	for _, line := range bytes.Split(bytes.TrimSuffix(w.Bytes(), []byte("\n")), []byte("\n")) {
		var msg struct{ ID int }
		if err := json.Unmarshal(line, &msg); err != nil || seen[msg.ID] {
			t.Fatalf("invalid or duplicate frame %q", line)
		}
		seen[msg.ID] = true
	}
	if len(seen) != 12 {
		t.Fatalf("received %d frames", len(seen))
	}
}

func TestStdioWriteFailureEndsStream(t *testing.T) {
	writes := 0
	s := &Session{writer: frameWriterFunc(func(p []byte) (int, error) {
		writes++
		if writes == 1 {
			return 1, nil
		}
		return 0, nil
	}), cancelLifetime: func() {}}
	for range 2 {
		if err := s.writeFrame([]byte("{}")); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("writeFrame returned %v", err)
		}
	}
	if writes != 2 {
		t.Fatalf("writer reused after incomplete frame: %d writes", writes)
	}
}

type shortFrameWriter struct{ bytes.Buffer }

func (w *shortFrameWriter) Write(p []byte) (int, error) {
	return w.Buffer.Write(p[:min(2, len(p))])
}

type frameWriterFunc func([]byte) (int, error)

func (f frameWriterFunc) Write(p []byte) (int, error) { return f(p) }
