package auth

import (
	"crypto/rand"
	"sync"
	"time"
)

// challengeStore issues and consumes single-use login/rekey nonces, bound to
// an auth_pub, per E2EE §3.4. In-process memory is sufficient for a single
// server instance (E2EE §11 data model notes this is acceptable in
// place of a DB table). Entries are keyed by auth_pub AND nonce, so anyone
// who knows a victim's auth_pub can neither replace the victim's pending
// challenge by requesting another nor erase it with a wrong nonce.
// Total size stays bounded by the per-IP challenge rate times the TTL.
type challengeStore struct {
	mu        sync.Mutex
	entries   map[string]time.Time // authPubKey + nonce -> expiry
	ttl       time.Duration
	lastSweep time.Time
}

// sweepInterval is how often issue drops expired challenges.
const sweepInterval = time.Minute

// newChallengeStore builds a store with the given nonce lifetime. Production
// uses 60 seconds (E2EE §3.4); tests inject a shorter TTL to exercise expiry
// without sleeping 60s.
func newChallengeStore(ttl time.Duration) *challengeStore {
	return &challengeStore{entries: make(map[string]time.Time), ttl: ttl}
}

func (s *challengeStore) issue(authPubKey string) ([]byte, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	// /auth/challenge is unauthenticated and keyed by caller-chosen
	// auth_pub, so abandoned challenges must not pile up forever.
	if now.Sub(s.lastSweep) >= sweepInterval {
		for k, exp := range s.entries {
			if now.After(exp) {
				delete(s.entries, k)
			}
		}
		s.lastSweep = now
	}
	s.entries[authPubKey+string(nonce)] = now.Add(s.ttl)
	return nonce, nil
}

// consume reports whether nonce is a live, unexpired challenge for
// authPubKey, and removes it (single-use). A nonce that was never issued
// removes nothing. The lookup is a hashed map key over a 256-bit random
// nonce, so it leaks nothing a constant-time compare would protect.
func (s *challengeStore) consume(authPubKey string, nonce []byte) bool {
	key := authPubKey + string(nonce)
	s.mu.Lock()
	exp, ok := s.entries[key]
	delete(s.entries, key)
	s.mu.Unlock()
	return ok && !time.Now().After(exp)
}
