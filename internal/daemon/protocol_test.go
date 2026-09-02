package daemon

import (
	"encoding/json"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/server"
)

// TestHandshakeWireShape pins the flattened encoding of the embedded
// server.SessionOptions. The tag names are the compatibility contract between
// a shim and a daemon built from different revisions, so renaming a Go field
// must not move a key.
func TestHandshakeWireShape(t *testing.T) {
	h := Handshake{
		Type: "session", Protocol: SessionProtocol, Build: "b", ConfigPath: "/c",
		SessionOptions: server.SessionOptions{
			Namespace: "work", EagerStart: true, ExposeManagerTools: true,
			ExposeResources: true, ExposePrompts: true,
			Compression: config.CompressionForce(config.CompressionMedium),
		},
		PID: 42,
	}
	encoded, err := json.Marshal(HandshakeEnvelope{Handshake: h})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]map[string]any
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	want := map[string]any{
		"type": "session", "protocol": float64(SessionProtocol), "build": "b",
		"configPath": "/c", "namespace": "work", "eager": true,
		"exposeManagerTools": true, "resources": true, "prompts": true,
		"compression": "medium", "pid": float64(42),
	}
	got := keys["mcpmu_handshake"]
	if len(got) != len(want) {
		t.Fatalf("handshake keys = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("handshake[%q] = %v, want %v", k, got[k], v)
		}
	}

	var back HandshakeEnvelope
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if back.Handshake != h {
		t.Fatalf("round trip = %+v, want %+v", back.Handshake, h)
	}
}

// TestHandshakeDecodesWithoutCompression covers a shim built before the
// --compress flag existed: it never sent the key, and an absent key must read
// as "flag not given" rather than as an explicit off.
func TestHandshakeDecodesWithoutCompression(t *testing.T) {
	line := `{"mcpmu_handshake":{"type":"session","protocol":1,"configPath":"/c",` +
		`"namespace":"n","eager":true,"exposeManagerTools":true,"resources":true,"prompts":true}}`
	var envelope HandshakeEnvelope
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	h := envelope.Handshake
	if h.Compression.Set() {
		t.Error("absent compression key decoded as an explicit override")
	}
	if h.Namespace != "n" || !h.EagerStart || !h.ExposeManagerTools || !h.ExposeResources || !h.ExposePrompts {
		t.Errorf("session options = %+v, want every flag carried across", h.SessionOptions)
	}
}
