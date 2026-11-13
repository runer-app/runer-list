package auth

import (
	"context"
	"sync/atomic"
	"time"

	"runer-api/internal/store"
)

// challengeStore issues and consumes single-use login nonces, bound to an
// auth_pub, per E2EE §3.4. They live in the database (store.Challenges),
// so a challenge issued by one server instance can be answered at another.
// Entries are keyed by auth_pub AND nonce, so anyone who knows a victim's
// auth_pub can neither replace the victim's pending challenge by requesting
// another nor erase it with a wrong nonce. Total size stays bounded by the
// per-IP challenge rate times the sweep interval.
type challengeStore struct {
	db        *store.Challenges
	ttl       time.Duration
	lastSweep atomic.Int64 // unix nanoseconds
}

// sweepInterval is how often issue drops expired challenges.
const sweepInterval = time.Minute

// newChallengeStore builds a store with the given nonce lifetime. Production
// uses 60 seconds (E2EE §3.4); tests inject a shorter TTL to exercise expiry
// without sleeping 60s.
func newChallengeStore(db *store.Challenges, ttl time.Duration) *challengeStore {
	return &challengeStore{db: db, ttl: ttl}
}

func (s *challengeStore) issue(ctx context.Context, authPubKey string) ([]byte, error) {
	// /auth/challenge is unauthenticated and keyed by caller-chosen
	// auth_pub, so abandoned challenges must not pile up forever. Each
	// instance sweeps at most once per interval.
	now := time.Now().UnixNano()
	if last := s.lastSweep.Load(); now-last >= int64(sweepInterval) && s.lastSweep.CompareAndSwap(last, now) {
		if _, err := s.db.SweepExpired(ctx); err != nil {
			return nil, err
		}
	}
	return s.db.Issue(ctx, store.ChallengeLogin, authPubKey, s.ttl, false)
}

// consume reports whether nonce is a live, unexpired challenge for
// authPubKey, and removes it (single-use, on every instance).
func (s *challengeStore) consume(ctx context.Context, authPubKey string, nonce []byte) (bool, error) {
	return s.db.Consume(ctx, store.ChallengeLogin, authPubKey, nonce)
}
