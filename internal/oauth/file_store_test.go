package oauth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStore_PutAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	cred := &Credential{
		ServerName:   "test-server",
		ServerURL:    "https://mcp.example.com/mcp",
		ClientID:     "client-123",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		Scopes:       []string{"read", "write"},
	}

	// Put
	if err := store.Put(cred); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get
	got, err := store.Get("https://mcp.example.com/mcp")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}

	if got.ServerName != cred.ServerName {
		t.Errorf("ServerName: got %q, want %q", got.ServerName, cred.ServerName)
	}
	if got.ClientID != cred.ClientID {
		t.Errorf("ClientID: got %q, want %q", got.ClientID, cred.ClientID)
	}
	if got.AccessToken != cred.AccessToken {
		t.Errorf("AccessToken: got %q, want %q", got.AccessToken, cred.AccessToken)
	}
}

func TestFileStore_GetNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	got, err := store.Get("https://nonexistent.com/mcp")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestFileStore_Update(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	cred := &Credential{
		ServerName:  "test-server",
		ServerURL:   "https://mcp.example.com/mcp",
		AccessToken: "token-1",
	}

	// Initial put
	if err := store.Put(cred); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Update
	cred.AccessToken = "token-2"
	if err := store.Put(cred); err != nil {
		t.Fatalf("Put update failed: %v", err)
	}

	// Verify
	got, err := store.Get(cred.ServerURL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.AccessToken != "token-2" {
		t.Errorf("AccessToken: got %q, want %q", got.AccessToken, "token-2")
	}

	// Verify only one entry
	list, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 entry, got %d", len(list))
	}
}

func TestFileStore_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	cred := &Credential{
		ServerName:  "test-server",
		ServerURL:   "https://mcp.example.com/mcp",
		AccessToken: "token",
	}

	// Put
	if err := store.Put(cred); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Delete
	if err := store.Delete(cred.ServerURL); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted
	got, err := store.Get(cred.ServerURL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestFileStore_List(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	// Add multiple
	for i, url := range []string{"https://a.com", "https://b.com", "https://c.com"} {
		cred := &Credential{
			ServerName:  "server-" + string(rune('a'+i)),
			ServerURL:   url,
			AccessToken: "token-" + string(rune('a'+i)),
		}
		if err := store.Put(cred); err != nil {
			t.Fatalf("Put %s failed: %v", url, err)
		}
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 entries, got %d", len(list))
	}
}

func TestFileStore_Permissions(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "creds.json")
	store := NewFileStoreAt(path)

	cred := &Credential{
		ServerName:  "test",
		ServerURL:   "https://test.com",
		AccessToken: "token",
	}

	if err := store.Put(cred); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Check file permissions
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	// Should be 0600 (owner read/write only)
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions: got %o, want 0600", perm)
	}
}

func TestCredential_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt int64
		want      bool
	}{
		{
			name:      "not expired",
			expiresAt: time.Now().Add(time.Hour).UnixMilli(),
			want:      false,
		},
		{
			name:      "expired",
			expiresAt: time.Now().Add(-time.Hour).UnixMilli(),
			want:      true,
		},
		{
			name:      "just expired",
			expiresAt: time.Now().Add(-time.Millisecond).UnixMilli(),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Credential{ExpiresAt: tt.expiresAt}
			if got := c.IsExpired(); got != tt.want {
				t.Errorf("IsExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCredential_NeedsRefresh(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt int64
		want      bool
	}{
		{
			name:      "far from expiry",
			expiresAt: time.Now().Add(time.Hour).UnixMilli(),
			want:      false,
		},
		{
			name:      "within 30 seconds",
			expiresAt: time.Now().Add(20 * time.Second).UnixMilli(),
			want:      true,
		},
		{
			name:      "already expired",
			expiresAt: time.Now().Add(-time.Hour).UnixMilli(),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Credential{ExpiresAt: tt.expiresAt}
			if got := c.NeedsRefresh(); got != tt.want {
				t.Errorf("NeedsRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}
