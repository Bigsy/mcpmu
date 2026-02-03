package oauth

import (
	"testing"
	"time"
)

func TestCredential_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cred    *Credential
		wantErr string
	}{
		{
			name: "valid credential",
			cred: &Credential{
				ServerURL:   "https://example.com/mcp",
				ClientID:    "client-123",
				AccessToken: "token-abc",
				ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
			},
			wantErr: "",
		},
		{
			name: "missing ServerURL",
			cred: &Credential{
				ClientID:    "client-123",
				AccessToken: "token-abc",
				ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
			},
			wantErr: "ServerURL is required",
		},
		{
			name: "missing ClientID",
			cred: &Credential{
				ServerURL:   "https://example.com/mcp",
				AccessToken: "token-abc",
				ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
			},
			wantErr: "ClientID is required",
		},
		{
			name: "missing AccessToken",
			cred: &Credential{
				ServerURL: "https://example.com/mcp",
				ClientID:  "client-123",
				ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
			},
			wantErr: "AccessToken is required",
		},
		{
			name: "zero ExpiresAt",
			cred: &Credential{
				ServerURL:   "https://example.com/mcp",
				ClientID:    "client-123",
				AccessToken: "token-abc",
				ExpiresAt:   0,
			},
			wantErr: "ExpiresAt must be a positive timestamp",
		},
		{
			name: "negative ExpiresAt",
			cred: &Credential{
				ServerURL:   "https://example.com/mcp",
				ClientID:    "client-123",
				AccessToken: "token-abc",
				ExpiresAt:   -1,
			},
			wantErr: "ExpiresAt must be a positive timestamp",
		},
		{
			name: "ServerName is optional",
			cred: &Credential{
				ServerURL:   "https://example.com/mcp",
				ClientID:    "client-123",
				AccessToken: "token-abc",
				ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
				// ServerName intentionally omitted
			},
			wantErr: "",
		},
		{
			name: "RefreshToken is optional",
			cred: &Credential{
				ServerURL:   "https://example.com/mcp",
				ClientID:    "client-123",
				AccessToken: "token-abc",
				ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
				// RefreshToken intentionally omitted
			},
			wantErr: "",
		},
		{
			name: "ClientSecret is optional",
			cred: &Credential{
				ServerURL:   "https://example.com/mcp",
				ClientID:    "client-123",
				AccessToken: "token-abc",
				ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
				// ClientSecret intentionally omitted
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cred.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Validate() error = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Errorf("Validate() error = nil, want error containing %q", tt.wantErr)
				} else if !contains(err.Error(), tt.wantErr) {
					t.Errorf("Validate() error = %v, want error containing %q", err, tt.wantErr)
				}
			}
		})
	}
}

func TestNewCredential(t *testing.T) {
	tests := []struct {
		name         string
		serverName   string
		serverURL    string
		clientID     string
		clientSecret string
		accessToken  string
		refreshToken string
		expiresAt    time.Time
		scopes       []string
		wantErr      string
	}{
		{
			name:         "valid credential",
			serverName:   "my-server",
			serverURL:    "https://example.com/mcp",
			clientID:     "client-123",
			clientSecret: "secret-456",
			accessToken:  "token-abc",
			refreshToken: "refresh-xyz",
			expiresAt:    time.Now().Add(time.Hour),
			scopes:       []string{"read", "write"},
			wantErr:      "",
		},
		{
			name:         "empty serverName is OK",
			serverName:   "",
			serverURL:    "https://example.com/mcp",
			clientID:     "client-123",
			clientSecret: "",
			accessToken:  "token-abc",
			refreshToken: "",
			expiresAt:    time.Now().Add(time.Hour),
			scopes:       nil,
			wantErr:      "",
		},
		{
			name:         "empty clientSecret is OK (public client)",
			serverName:   "my-server",
			serverURL:    "https://example.com/mcp",
			clientID:     "client-123",
			clientSecret: "",
			accessToken:  "token-abc",
			refreshToken: "",
			expiresAt:    time.Now().Add(time.Hour),
			scopes:       nil,
			wantErr:      "",
		},
		{
			name:         "missing serverURL",
			serverName:   "my-server",
			serverURL:    "",
			clientID:     "client-123",
			clientSecret: "",
			accessToken:  "token-abc",
			refreshToken: "",
			expiresAt:    time.Now().Add(time.Hour),
			scopes:       nil,
			wantErr:      "ServerURL is required",
		},
		{
			name:         "missing clientID",
			serverName:   "my-server",
			serverURL:    "https://example.com/mcp",
			clientID:     "",
			clientSecret: "",
			accessToken:  "token-abc",
			refreshToken: "",
			expiresAt:    time.Now().Add(time.Hour),
			scopes:       nil,
			wantErr:      "ClientID is required",
		},
		{
			name:         "missing accessToken",
			serverName:   "my-server",
			serverURL:    "https://example.com/mcp",
			clientID:     "client-123",
			clientSecret: "",
			accessToken:  "",
			refreshToken: "",
			expiresAt:    time.Now().Add(time.Hour),
			scopes:       nil,
			wantErr:      "AccessToken is required",
		},
		{
			name:         "zero time fails",
			serverName:   "my-server",
			serverURL:    "https://example.com/mcp",
			clientID:     "client-123",
			clientSecret: "",
			accessToken:  "token-abc",
			refreshToken: "",
			expiresAt:    time.Time{},
			scopes:       nil,
			wantErr:      "ExpiresAt must be a positive timestamp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cred, err := NewCredential(
				tt.serverName,
				tt.serverURL,
				tt.clientID,
				tt.clientSecret,
				tt.accessToken,
				tt.refreshToken,
				tt.expiresAt,
				tt.scopes,
			)

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("NewCredential() error = %v, want nil", err)
					return
				}
				if cred == nil {
					t.Fatal("NewCredential() returned nil credential without error")
				}
				// Verify fields are set correctly
				if cred.ServerName != tt.serverName {
					t.Errorf("ServerName = %q, want %q", cred.ServerName, tt.serverName)
				}
				if cred.ServerURL != tt.serverURL {
					t.Errorf("ServerURL = %q, want %q", cred.ServerURL, tt.serverURL)
				}
				if cred.ClientID != tt.clientID {
					t.Errorf("ClientID = %q, want %q", cred.ClientID, tt.clientID)
				}
				if cred.ClientSecret != tt.clientSecret {
					t.Errorf("ClientSecret = %q, want %q", cred.ClientSecret, tt.clientSecret)
				}
				if cred.AccessToken != tt.accessToken {
					t.Errorf("AccessToken = %q, want %q", cred.AccessToken, tt.accessToken)
				}
				if cred.RefreshToken != tt.refreshToken {
					t.Errorf("RefreshToken = %q, want %q", cred.RefreshToken, tt.refreshToken)
				}
				if cred.ExpiresAt != tt.expiresAt.UnixMilli() {
					t.Errorf("ExpiresAt = %d, want %d", cred.ExpiresAt, tt.expiresAt.UnixMilli())
				}
			} else {
				if err == nil {
					t.Errorf("NewCredential() error = nil, want error containing %q", tt.wantErr)
				} else if !contains(err.Error(), tt.wantErr) {
					t.Errorf("NewCredential() error = %v, want error containing %q", err, tt.wantErr)
				}
				if cred != nil {
					t.Errorf("NewCredential() returned non-nil credential with error")
				}
			}
		})
	}
}

func TestFileStore_Put_Validation(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStoreAt(tmpDir + "/creds.json")

	tests := []struct {
		name    string
		cred    *Credential
		wantErr string
	}{
		{
			name: "valid credential succeeds",
			cred: &Credential{
				ServerURL:   "https://example.com/mcp",
				ClientID:    "client-123",
				AccessToken: "token-abc",
				ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
			},
			wantErr: "",
		},
		{
			name: "missing ServerURL rejected",
			cred: &Credential{
				ClientID:    "client-123",
				AccessToken: "token-abc",
				ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
			},
			wantErr: "ServerURL is required",
		},
		{
			name: "missing ClientID rejected",
			cred: &Credential{
				ServerURL:   "https://example.com/mcp",
				AccessToken: "token-abc",
				ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
			},
			wantErr: "ClientID is required",
		},
		{
			name: "missing AccessToken rejected",
			cred: &Credential{
				ServerURL: "https://example.com/mcp",
				ClientID:  "client-123",
				ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
			},
			wantErr: "AccessToken is required",
		},
		{
			name: "zero ExpiresAt rejected",
			cred: &Credential{
				ServerURL:   "https://example.com/mcp",
				ClientID:    "client-123",
				AccessToken: "token-abc",
				ExpiresAt:   0,
			},
			wantErr: "ExpiresAt must be a positive timestamp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.Put(tt.cred)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Put() error = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Errorf("Put() error = nil, want error containing %q", tt.wantErr)
				} else if !contains(err.Error(), tt.wantErr) {
					t.Errorf("Put() error = %v, want error containing %q", err, tt.wantErr)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
