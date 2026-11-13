package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Challenge kinds.
const (
	ChallengeLogin = "login"
	ChallengeRekey = "rekey"
)

// Challenges stores single-use nonces shared by every server instance.
// Expiry is the database's clock, so instances need not agree on time.
type Challenges struct {
	pool *pgxpool.Pool
}

func NewChallenges(pool *pgxpool.Pool) *Challenges {
	return &Challenges{pool: pool}
}

// Issue stores a fresh 32-byte nonce for (kind, subject), live for ttl.
// With replace, it first drops the subject's other pending nonces of kind,
// so only the newest can be answered.
func (s *Challenges) Issue(ctx context.Context, kind, subject string, ttl time.Duration, replace bool) ([]byte, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	insert := `INSERT INTO challenges (kind, subject, nonce, expires_at)
		VALUES ($1, $2, $3, now() + $4 * interval '1 microsecond')`
	if replace {
		insert = `WITH gone AS (DELETE FROM challenges WHERE kind = $1 AND subject = $2) ` + insert
	}
	if _, err := s.pool.Exec(ctx, insert, kind, subject, nonce, ttl.Microseconds()); err != nil {
		return nil, fmt.Errorf("issue challenge: %w", err)
	}
	return nonce, nil
}

// Consume reports whether nonce is a live challenge for (kind, subject)
// and removes it, so it answers once, on whichever instance gets there
// first. A nonce never issued removes nothing. Keyed by the nonce, so
// someone who knows a victim's subject can't erase the victim's pending
// challenge with a wrong one; the lookup is an index probe on a 256-bit
// random value, which leaks nothing a constant-time compare would protect.
func (s *Challenges) Consume(ctx context.Context, kind, subject string, nonce []byte) (bool, error) {
	var live bool
	err := s.pool.QueryRow(ctx, `DELETE FROM challenges WHERE kind = $1 AND subject = $2 AND nonce = $3
		RETURNING expires_at > now()`, kind, subject, nonce).Scan(&live)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("consume challenge: %w", err)
	}
	return live, nil
}

// ConsumeAny removes every pending challenge of kind for subject and
// reports whether nonce was one of them and live. For a subject that is
// already authenticated (a rekey's account): any attempt, right or wrong,
// uses the challenge up.
func (s *Challenges) ConsumeAny(ctx context.Context, kind, subject string, nonce []byte) (bool, error) {
	rows, err := s.pool.Query(ctx, `DELETE FROM challenges WHERE kind = $1 AND subject = $2
		RETURNING nonce, expires_at > now()`, kind, subject)
	if err != nil {
		return false, fmt.Errorf("consume challenge: %w", err)
	}
	defer rows.Close()
	ok := false
	for rows.Next() {
		var n []byte
		var live bool
		if err := rows.Scan(&n, &live); err != nil {
			return false, fmt.Errorf("consume challenge: %w", err)
		}
		if live && subtle.ConstantTimeCompare(n, nonce) == 1 {
			ok = true
		}
	}
	return ok, rows.Err()
}

// SweepExpired deletes expired challenges and returns how many. Abandoned
// login challenges are keyed by a caller-chosen auth_pub, so they must not
// pile up.
func (s *Challenges) SweepExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM challenges WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("sweep challenges: %w", err)
	}
	return tag.RowsAffected(), nil
}
