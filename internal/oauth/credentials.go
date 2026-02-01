// Package oauth provides OAuth 2.1 authentication for MCP servers.
package oauth

import (
	"time"
)

// Credential represents stored OAuth credentials for a server.
type Credential struct {
	// ServerName is the user-facing name of the server.
	ServerName string `json:"server_name"`

	// ServerURL is the URL of the MCP server (used for lookup).
	ServerURL string `json:"server_url"`

	// ClientID is from dynamic client registration.
	ClientID string `json:"client_id"`

	// AccessToken is the current access token.
	AccessToken string `json:"access_token"`

	// RefreshToken is used to obtain new access tokens.
	RefreshToken string `json:"refresh_token,omitempty"`

	// ExpiresAt is when the access token expires (Unix milliseconds).
	ExpiresAt int64 `json:"expires_at"`

	// Scopes are the granted OAuth scopes.
	Scopes []string `json:"scopes,omitempty"`
}

// IsExpired returns true if the access token has expired.
func (c Credential) IsExpired() bool {
	return time.Now().UnixMilli() >= c.ExpiresAt
}

// NeedsRefresh returns true if the token should be refreshed.
// Tokens are refreshed 30 seconds before expiry.
func (c Credential) NeedsRefresh() bool {
	return time.Now().UnixMilli() >= (c.ExpiresAt - 30000)
}

// TimeUntilExpiry returns the duration until the token expires.
func (c Credential) TimeUntilExpiry() time.Duration {
	return time.Until(time.UnixMilli(c.ExpiresAt))
}

// CredentialStore is the interface for OAuth credential storage.
type CredentialStore interface {
	// Get retrieves credentials for a server by URL.
	Get(serverURL string) (*Credential, error)

	// Put stores credentials for a server.
	Put(cred *Credential) error

	// Delete removes credentials for a server.
	Delete(serverURL string) error

	// List returns all stored credentials.
	List() ([]*Credential, error)
}

// StoreMode represents the credential storage mode.
type StoreMode string

const (
	// StoreModeAuto uses keyring if available, falls back to file.
	StoreModeAuto StoreMode = "auto"

	// StoreModeKeyring uses the system keychain.
	StoreModeKeyring StoreMode = "keyring"

	// StoreModeFile uses a JSON file.
	StoreModeFile StoreMode = "file"
)

// NewCredentialStore creates a credential store based on the mode.
func NewCredentialStore(mode StoreMode) (CredentialStore, error) {
	switch mode {
	case StoreModeKeyring:
		store, err := NewKeyringStore()
		if err != nil {
			return nil, err
		}
		return store, nil

	case StoreModeFile:
		return NewFileStore()

	case StoreModeAuto, "":
		// Try keyring first, fall back to file
		store, err := NewKeyringStore()
		if err == nil {
			return store, nil
		}
		return NewFileStore()

	default:
		return NewFileStore()
	}
}
