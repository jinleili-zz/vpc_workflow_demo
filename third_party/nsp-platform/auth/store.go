// Package auth provides AK/SK authentication functionality for NSP platform.
// It includes credential storage, nonce-based replay attack prevention,
// HMAC-SHA256 signature signing/verification, and Gin middleware integration.
package auth

import (
	"context"
	"sync"
)

// Credential represents an AK/SK credential pair with metadata.
type Credential struct {
	// AccessKey is the public identifier transmitted with requests.
	AccessKey string
	// SecretKey is the private key stored server-side, never transmitted.
	SecretKey string
	// Label is a description of the credential, e.g., "nsp-order-service".
	Label string
	// Enabled indicates whether the credential is active. False means authentication is rejected.
	Enabled bool
}

// CredentialStore defines the interface for credential storage and retrieval.
type CredentialStore interface {
	// GetByAK retrieves a credential by its AccessKey.
	// Returns (nil, nil) if the credential is not found.
	// Returns (nil, err) if an error occurs during retrieval.
	GetByAK(ctx context.Context, ak string) (*Credential, error)
}

// MemoryStore is an in-memory implementation of CredentialStore.
// It is thread-safe and supports runtime credential registration.
type MemoryStore struct {
	mu    sync.RWMutex
	creds map[string]*Credential
}

// NewMemoryStore creates a new MemoryStore with optional preloaded credentials.
func NewMemoryStore(creds []*Credential) *MemoryStore {
	store := &MemoryStore{
		creds: make(map[string]*Credential),
	}
	for _, c := range creds {
		if c != nil {
			store.creds[c.AccessKey] = c
		}
	}
	return store
}

// GetByAK retrieves a credential by its AccessKey.
// Returns (nil, nil) if the credential is not found.
func (s *MemoryStore) GetByAK(ctx context.Context, ak string) (*Credential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cred, exists := s.creds[ak]
	if !exists {
		return nil, nil
	}
	return cred, nil
}

// Add registers a new credential at runtime.
// If a credential with the same AccessKey already exists, it will be overwritten.
func (s *MemoryStore) Add(cred *Credential) {
	if cred == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[cred.AccessKey] = cred
}
