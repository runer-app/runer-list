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
	UserAgent  *string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// Expired reports whether the session is unusable at t: revoked, past its
// absolute ceiling, or idle for longer than SessionIdleTimeout.
func (s Session) Expired(t time.Time) bool {
	if s.RevokedAt != nil {
		return true
	}
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

const sessionColumns = `id, account_id, token_hash, user_agent, created_at, last_seen_at, expires_at, revoked_at`

func scanSession(row pgx.Row) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.AccountID, &s.TokenHash, &s.UserAgent, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.RevokedAt)
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
func (s *Sessions) Create(ctx context.Context, accountID string, authPub, tokenHash []byte, userAgent string) (Session, error) {
	row := s.pool.QueryRow(ctx, `INSERT INTO sessions (account_id, token_hash, user_agent, expires_at)
		SELECT $1, $2, $3, now() + $4::interval
		WHERE EXISTS (SELECT 1 FROM accounts WHERE id = $1 AND auth_pub = $5 AND deleted_at IS NULL FOR SHARE)
		RETURNING `+sessionColumns,
		accountID, tokenHash, userAgent, fmt.Sprintf("%d seconds", int(SessionAbsoluteTimeout.Seconds())), authPub)
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

func (s *Sessions) ListByAccount(ctx context.Context, accountID string) ([]Session, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sessionColumns+` FROM sessions
		WHERE account_id = $1 AND revoked_at IS NULL ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Revoke marks a session revoked if it belongs to accountID. Returns
// ErrNotFound if no such session exists for that account.
func (s *Sessions) Revoke(ctx context.Context, id, accountID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked_at = now()
		WHERE id = $1 AND account_id = $2 AND revoked_at IS NULL`, id, accountID)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
