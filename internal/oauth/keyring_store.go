package oauth

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

const (
	// keyringService is the service name used in the system keychain.
	keyringService = "mcpmu"

	// keyringIndexKey is the key used to store the list of server URLs.
	keyringIndexKey = "_index"
)

// KeyringStore stores credentials in the system keychain.
type KeyringStore struct {
	mu sync.RWMutex
}

// NewKeyringStore creates a new keyring-based credential store.
// Returns an error if the keyring is not available.
func NewKeyringStore() (*KeyringStore, error) {
	// Test keyring availability by trying to read a non-existent key
	_, err := keyring.Get(keyringService, "_test_availability")
	if err != nil && err != keyring.ErrNotFound {
		return nil, fmt.Errorf("keyring not available: %w", err)
	}

	return &KeyringStore{}, nil
}

// Get retrieves credentials for a server by URL.
func (s *KeyringStore) Get(serverURL string) (*Credential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := urlToKey(serverURL)
	data, err := keyring.Get(keyringService, key)
	if err != nil {
		if err == keyring.ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("keyring get: %w", err)
	}

	var cred Credential
	if err := json.Unmarshal([]byte(data), &cred); err != nil {
		return nil, fmt.Errorf("parse credential: %w", err)
	}

	return &cred, nil
}

// Put stores credentials for a server.
func (s *KeyringStore) Put(cred *Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}

	key := urlToKey(cred.ServerURL)
	if err := keyring.Set(keyringService, key, string(data)); err != nil {
		return fmt.Errorf("keyring set: %w", err)
	}

	// Update index
	return s.addToIndex(cred.ServerURL)
}

// Delete removes credentials for a server.
func (s *KeyringStore) Delete(serverURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := urlToKey(serverURL)
	if err := keyring.Delete(keyringService, key); err != nil {
		if err != keyring.ErrNotFound {
			return fmt.Errorf("keyring delete: %w", err)
		}
	}

	// Update index
	return s.removeFromIndex(serverURL)
}

// List returns all stored credentials.
func (s *KeyringStore) List() ([]*Credential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	urls, err := s.loadIndex()
	if err != nil {
		return nil, err
	}

	creds := make([]*Credential, 0, len(urls))
	for _, url := range urls {
		key := urlToKey(url)
		data, err := keyring.Get(keyringService, key)
		if err != nil {
			if err == keyring.ErrNotFound {
				continue // Skip missing entries
			}
			return nil, fmt.Errorf("keyring get %s: %w", url, err)
		}

		var cred Credential
		if err := json.Unmarshal([]byte(data), &cred); err != nil {
			continue // Skip corrupted entries
		}

		creds = append(creds, &cred)
	}

	return creds, nil
}

// loadIndex reads the list of stored server URLs (caller must hold lock).
func (s *KeyringStore) loadIndex() ([]string, error) {
	data, err := keyring.Get(keyringService, keyringIndexKey)
	if err != nil {
		if err == keyring.ErrNotFound {
			return []string{}, nil
		}
		return nil, fmt.Errorf("keyring get index: %w", err)
	}

	if data == "" {
		return []string{}, nil
	}

	var urls []string
	if err := json.Unmarshal([]byte(data), &urls); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}

	return urls, nil
}

// saveIndex writes the list of stored server URLs (caller must hold lock).
func (s *KeyringStore) saveIndex(urls []string) error {
	data, err := json.Marshal(urls)
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}

	if err := keyring.Set(keyringService, keyringIndexKey, string(data)); err != nil {
		return fmt.Errorf("keyring set index: %w", err)
	}

	return nil
}

// addToIndex adds a URL to the index (caller must hold lock).
func (s *KeyringStore) addToIndex(url string) error {
	urls, err := s.loadIndex()
	if err != nil {
		return err
	}

	// Check if already exists
	for _, u := range urls {
		if u == url {
			return nil
		}
	}

	urls = append(urls, url)
	return s.saveIndex(urls)
}

// removeFromIndex removes a URL from the index (caller must hold lock).
func (s *KeyringStore) removeFromIndex(url string) error {
	urls, err := s.loadIndex()
	if err != nil {
		return err
	}

	filtered := make([]string, 0, len(urls))
	for _, u := range urls {
		if u != url {
			filtered = append(filtered, u)
		}
	}

	return s.saveIndex(filtered)
}

// urlToKey converts a server URL to a keyring key.
// URLs are sanitized to be valid keyring keys.
func urlToKey(url string) string {
	// Replace problematic characters
	key := strings.ReplaceAll(url, "://", "_")
	key = strings.ReplaceAll(key, "/", "_")
	key = strings.ReplaceAll(key, ":", "_")
	return key
}
