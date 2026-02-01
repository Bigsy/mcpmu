package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	credentialsDir  = ".config/mcpmu"
	credentialsFile = ".credentials.json"
)

// FileStore stores credentials in a JSON file.
type FileStore struct {
	path string
	mu   sync.RWMutex
}

// NewFileStore creates a new file-based credential store.
func NewFileStore() (*FileStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}

	path := filepath.Join(home, credentialsDir, credentialsFile)
	return &FileStore{path: path}, nil
}

// NewFileStoreAt creates a file store at a specific path (for testing).
func NewFileStoreAt(path string) *FileStore {
	return &FileStore{path: path}
}

// Get retrieves credentials for a server by URL.
func (s *FileStore) Get(serverURL string) (*Credential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	creds, err := s.load()
	if err != nil {
		return nil, err
	}

	for _, c := range creds {
		if c.ServerURL == serverURL {
			return c, nil
		}
	}

	return nil, nil
}

// Put stores credentials for a server.
func (s *FileStore) Put(cred *Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.load()
	if err != nil {
		return err
	}

	// Update or append
	found := false
	for i, c := range creds {
		if c.ServerURL == cred.ServerURL {
			creds[i] = cred
			found = true
			break
		}
	}
	if !found {
		creds = append(creds, cred)
	}

	return s.save(creds)
}

// Delete removes credentials for a server.
func (s *FileStore) Delete(serverURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.load()
	if err != nil {
		return err
	}

	// Filter out the credential
	filtered := make([]*Credential, 0, len(creds))
	for _, c := range creds {
		if c.ServerURL != serverURL {
			filtered = append(filtered, c)
		}
	}

	return s.save(filtered)
}

// List returns all stored credentials.
func (s *FileStore) List() ([]*Credential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.load()
}

// load reads credentials from the file (caller must hold lock).
func (s *FileStore) load() ([]*Credential, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Credential{}, nil
		}
		return nil, fmt.Errorf("read credentials: %w", err)
	}

	var creds []*Credential
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	return creds, nil
}

// save writes credentials to the file (caller must hold lock).
func (s *FileStore) save(creds []*Credential) error {
	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	// Write to temp file first
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename credentials: %w", err)
	}

	return nil
}
