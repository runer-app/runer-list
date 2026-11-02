package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Session rules per E2EE §3.4: idle timeout and an absolute ceiling, whichever
// comes first.
const (
	SessionIdleTimeout     = 30 * 24 * time.Hour
	SessionAbsoluteTimeout = 180 * 24 * time.Hour
)

type Session struct {
	ID         string
	AccountID  string
	TokenHash  []byte
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// Expired reports whether the session is unusable at t: past its absolute
// ceiling, or idle for longer than SessionIdleTimeout.
func (s Session) Expired(t time.Time) bool {
	if t.After(s.ExpiresAt) {
		return true
	}
	return t.After(s.LastSeenAt.Add(SessionIdleTimeout))
}

type Sessions struct {
	pool *pgxpool.Pool
}

func NewSessions(pool *pgxpool.Pool) *Sessions {
	return &Sessions{pool: pool}
}

const sessionColumns = `id, account_id, token_hash, created_at, last_seen_at, expires_at`

func scanSession(row pgx.Row) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.AccountID, &s.TokenHash, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("scan session: %w", err)
	}
	return s, nil
}

// Create starts a session, but only while the account is live and its
// auth key is still authPub. The FOR SHARE lock orders this against a
// concurrent rekey: a rekey that commits first makes this insert
// nothing (ErrNotFound); one that commits later revokes this session.
func (s *Sessions) Create(ctx context.Context, accountID string, authPub, tokenHash []byte) (Session, error) {
	row := s.pool.QueryRow(ctx, `INSERT INTO sessions (account_id, token_hash, expires_at)
		SELECT $1, $2, now() + $3::interval
		WHERE EXISTS (SELECT 1 FROM accounts WHERE id = $1 AND auth_pub = $4 AND deleted_at IS NULL FOR SHARE)
		RETURNING `+sessionColumns,
		accountID, tokenHash, fmt.Sprintf("%d seconds", int(SessionAbsoluteTimeout.Seconds())), authPub)
	return scanSession(row)
}

func (s *Sessions) GetByTokenHash(ctx context.Context, tokenHash []byte) (Session, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE token_hash = $1`, tokenHash)
	return scanSession(row)
}

func (s *Sessions) GetByID(ctx context.Context, id string) (Session, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = $1`, id)
	return scanSession(row)
}

// TouchLastSeen refreshes last_seen_at at most once a minute: the idle
// timeout is measured to within a minute, and most requests write nothing.
func (s *Sessions) TouchLastSeen(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = now()
		WHERE id = $1 AND last_seen_at < now() - interval '1 minute'`, id)
	if err != nil {
		return fmt.Errorf("touch session last_seen: %w", err)
	}
	return nil
}

// Delete ends a session if it belongs to accountID. Returns ErrNotFound if
// no such session exists for that account.
func (s *Sessions) Delete(ctx context.Context, id, accountID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1 AND account_id = $2`, id, accountID)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
