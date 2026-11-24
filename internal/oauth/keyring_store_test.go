package oauth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func keyringTestCredential(tokenLen int) *Credential {
	return &Credential{
		ServerName:   "Atlassian",
		ServerURL:    "https://mcp.example.com/v2/mcp",
		ClientID:     "client-123",
		AccessToken:  strings.Repeat("a", tokenLen),
		RefreshToken: strings.Repeat("r", tokenLen),
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		Scopes:       []string{"read:jira", "offline_access"},
	}
}

func TestKeyringStore_ChunksOversizedCredential(t *testing.T) {
	keyring.MockInit()
	store := &KeyringStore{}
	big := keyringTestCredential(3000)

	if err := store.Put(big); err != nil {
		t.Fatalf("Put: %v", err)
	}

	key := urlToKey(big.ServerURL)
	marker, err := keyring.Get(keyringService, key)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	gen, count, ok := parseChunkMarker(marker)
	if !ok || count < 2 {
		t.Fatalf("main item = %q, want a chunk marker", marker)
	}
	for i := range count {
		part, err := keyring.Get(keyringService, chunkKey(key, gen, i))
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		if len(part) > keyringChunkSize {
			t.Errorf("chunk %d is %d bytes, over %d", i, len(part), keyringChunkSize)
		}
	}

	got, err := store.Get(big.ServerURL)
	if err != nil || got == nil {
		t.Fatalf("Get: %v, %v", got, err)
	}
	if got.AccessToken != big.AccessToken || got.RefreshToken != big.RefreshToken {
		t.Error("tokens did not round-trip through chunks")
	}
	listed, err := store.List()
	if err != nil || len(listed) != 1 || listed[0].AccessToken != big.AccessToken {
		t.Fatalf("List = %v, %v; want the chunked credential", listed, err)
	}

	// Replacing it with a small credential stores it inline and drops the chunks.
	if err := store.Put(keyringTestCredential(10)); err != nil {
		t.Fatalf("Put small: %v", err)
	}
	if v, _ := keyring.Get(keyringService, key); !strings.HasPrefix(v, "{") {
		t.Errorf("main item = %q, want inline JSON", v)
	}
	for i := range count {
		if _, err := keyring.Get(keyringService, chunkKey(key, gen, i)); !errors.Is(err, keyring.ErrNotFound) {
			t.Errorf("stale chunk %d left behind (err=%v)", i, err)
		}
	}
}

func TestKeyringStore_DeleteRemovesChunks(t *testing.T) {
	keyring.MockInit()
	store := &KeyringStore{}
	big := keyringTestCredential(3000)
	if err := store.Put(big); err != nil {
		t.Fatalf("Put: %v", err)
	}
	key := urlToKey(big.ServerURL)
	marker, _ := keyring.Get(keyringService, key)
	gen, count, _ := parseChunkMarker(marker)

	if err := store.Delete(big.ServerURL); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, err := store.Get(big.ServerURL); err != nil || got != nil {
		t.Errorf("Get after Delete = %v, %v; want nil, nil", got, err)
	}
	for i := range count {
		if _, err := keyring.Get(keyringService, chunkKey(key, gen, i)); !errors.Is(err, keyring.ErrNotFound) {
			t.Errorf("chunk %d survived Delete (err=%v)", i, err)
		}
	}
}
