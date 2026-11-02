package account

import (
	"crypto/rand"
	"crypto/subtle"
	"sync"
	"time"
)

// rekeyChallenges issues single-use nonces keyed by account id, for the
// rekey endpoint's proof-of-possession of the OLD auth key (E2EE §9.2).
// Same shape as internal/auth's challengeStore but keyed by account id
// (already authenticated by session) rather than auth_pub (not yet known to
// be authenticated); small enough not to be worth sharing across packages.
type rekeyChallenges struct {
	mu      sync.Mutex
	entries map[string]rekeyEntry
	ttl     time.Duration
}

type rekeyEntry struct {
	nonce     []byte
	expiresAt time.Time
}

func newRekeyChallenges() *rekeyChallenges {
	return &rekeyChallenges{entries: make(map[string]rekeyEntry), ttl: 60 * time.Second}
}

func (s *rekeyChallenges) issue(accountID string) ([]byte, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.entries[accountID] = rekeyEntry{nonce: nonce, expiresAt: time.Now().Add(s.ttl)}
	s.mu.Unlock()
	return nonce, nil
}

func (s *rekeyChallenges) consume(accountID string, nonce []byte) bool {
	s.mu.Lock()
	e, ok := s.entries[accountID]
	delete(s.entries, accountID)
	s.mu.Unlock()

	if !ok || time.Now().After(e.expiresAt) {
		return false
	}
	return subtle.ConstantTimeCompare(e.nonce, nonce) == 1
}
