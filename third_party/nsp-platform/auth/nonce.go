// nonce.go - Nonce-based replay attack prevention for AK/SK authentication.
package auth

import (
	"context"
	"sync"
	"time"
)

// NonceStore defines the interface for nonce storage and validation.
// It is used to prevent replay attacks by ensuring each nonce is only used once.
type NonceStore interface {
	// CheckAndStore checks if a nonce has been used within the TTL period.
	// If not used: stores the nonce and returns (false, nil).
	// If already used and not expired: returns (true, nil).
	// Returns (false, err) if an error occurs during the operation.
	CheckAndStore(ctx context.Context, nonce string, ttl time.Duration) (used bool, err error)
}

// nonceEntry represents a stored nonce with its expiration time.
type nonceEntry struct {
	expiresAt time.Time
}

// MemoryNonceStore is an in-memory implementation of NonceStore.
// It uses a background goroutine to periodically clean up expired nonces.
//
// WARNING: For production multi-instance deployments, use a Redis-based
// implementation. MemoryNonceStore does not share state across instances,
// which means replay protection is ineffective when requests hit different instances.
type MemoryNonceStore struct {
	mu      sync.Mutex
	nonces  map[string]nonceEntry
	stopCh  chan struct{}
	stopped bool
}

// NewMemoryNonceStore creates a new MemoryNonceStore and starts the cleanup goroutine.
// The cleanup goroutine runs every 5 minutes to remove expired nonces.
func NewMemoryNonceStore() *MemoryNonceStore {
	store := &MemoryNonceStore{
		nonces: make(map[string]nonceEntry),
		stopCh: make(chan struct{}),
	}
	go store.cleanupLoop()
	return store
}

// CheckAndStore checks if a nonce has been used and stores it if not.
// Returns (true, nil) if the nonce has already been used (replay attack detected).
// Returns (false, nil) if the nonce is new and has been successfully stored.
//
// Security note: Even expired nonces are rejected to prevent replay attacks
// where an attacker waits for the nonce TTL to expire before replaying.
// Nonces are kept for 2x TTL before being cleaned up.
func (s *MemoryNonceStore) CheckAndStore(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Check if nonce exists - reject both active AND recently expired nonces
	// to prevent replay attacks where attacker waits for TTL to expire
	if _, exists := s.nonces[nonce]; exists {
		// Nonce was used before - this is a replay attack
		return true, nil
	}

	// Store the nonce with extended expiration time (2x TTL) for safety margin
	// This ensures nonces are kept long enough to detect delayed replay attempts
	s.nonces[nonce] = nonceEntry{
		expiresAt: now.Add(ttl * 2),
	}

	return false, nil
}

// cleanupLoop runs periodically to remove expired nonces from memory.
// It runs every 5 minutes to prevent unbounded memory growth.
func (s *MemoryNonceStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanup()
		case <-s.stopCh:
			return
		}
	}
}

// cleanup removes all expired nonces from the store.
func (s *MemoryNonceStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for nonce, entry := range s.nonces {
		if now.After(entry.expiresAt) {
			delete(s.nonces, nonce)
		}
	}
}

// Stop gracefully stops the cleanup goroutine.
// This should be called when the MemoryNonceStore is no longer needed.
func (s *MemoryNonceStore) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.stopped {
		s.stopped = true
		close(s.stopCh)
	}
}
