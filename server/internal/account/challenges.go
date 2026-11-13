package account

import (
	"context"
	"time"

	"runer-api/internal/store"
)

// rekeyChallengeTTL is how long a rekey nonce can be answered (E2EE §9.2).
const rekeyChallengeTTL = 60 * time.Second

// rekeyChallenges issues single-use nonces keyed by account id, for the
// rekey endpoint's proof-of-possession of the OLD auth key (E2EE §9.2).
// Kept in the database (store.Challenges), so any instance can answer one.
// Unlike login challenges, the account is already authenticated by
// session: a new challenge replaces the pending one, and any attempt uses
// it up.
type rekeyChallenges struct {
	db *store.Challenges
}

func (s *rekeyChallenges) issue(ctx context.Context, accountID string) ([]byte, error) {
	return s.db.Issue(ctx, store.ChallengeRekey, accountID, rekeyChallengeTTL, true)
}

func (s *rekeyChallenges) consume(ctx context.Context, accountID string, nonce []byte) (bool, error) {
	return s.db.ConsumeAny(ctx, store.ChallengeRekey, accountID, nonce)
}
