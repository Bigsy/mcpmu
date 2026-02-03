// Package oauth provides OAuth 2.1 authentication for MCP servers.
package oauth

import (
	"errors"
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

	// ClientSecret is from dynamic client registration (may be empty for public clients).
	ClientSecret string `json:"client_secret,omitempty"`

	// AccessToken is the current access token.
	AccessToken string `json:"access_token"`

	// RefreshToken is used to obtain new access tokens.
	RefreshToken string `json:"refresh_token,omitempty"`

	// ExpiresAt is when the access token expires (Unix milliseconds).
	ExpiresAt int64 `json:"expires_at"`

	// Scopes are the granted OAuth scopes.
	Scopes []string `json:"scopes,omitempty"`
}

// Validate checks that all required fields are set and valid.
func (c *Credential) Validate() error {
	if c.ServerURL == "" {
		return errors.New("credential: ServerURL is required")
	}
	if c.ClientID == "" {
		return errors.New("credential: ClientID is required")
	}
	if c.AccessToken == "" {
		return errors.New("credential: AccessToken is required")
	}
	if c.ExpiresAt <= 0 {
		return errors.New("credential: ExpiresAt must be a positive timestamp")
	}
	return nil
}

// NewCredential creates a new Credential with validation.
// ServerName is optional (may be empty).
// RefreshToken, ClientSecret, and Scopes are optional.
func NewCredential(serverName, serverURL, clientID, clientSecret, accessToken, refreshToken string, expiresAt time.Time, scopes []string) (*Credential, error) {
	cred := &Credential{
		ServerName:   serverName,
		ServerURL:    serverURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt.UnixMilli(),
		Scopes:       scopes,
	}
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	return cred, nil
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
