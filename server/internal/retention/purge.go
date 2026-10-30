// Package retention implements the daily purge job: deleted lists,
// accounts and old sessions are purged after the retention window. The clock is injectable so tests
// can simulate time passing without sleeping.
package retention

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"runer-api/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Clock supplies "now". Production uses RealClock; tests inject a fixed
// or advancing fake so expiry is exercised deterministically.
type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// DefaultWindow is how long a soft-deleted row survives before this job
// purges it.
const DefaultWindow = 30 * 24 * time.Hour

type Purger struct {
	pool   *pgxpool.Pool
	clock  Clock
	window time.Duration
}

func NewPurger(pool *pgxpool.Pool, clock Clock, window time.Duration) *Purger {
	return &Purger{pool: pool, clock: clock, window: window}
}

// Report counts what PurgeOnce actually removed, for logging/tests.
type Report struct {
	Groups   int
	Accounts int
	Sessions int
}

// PurgeOnce runs a single pass. It is safe to call repeatedly (e.g. daily)
// and safe to call concurrently with normal request traffic — every
// delete is scoped to rows already soft-deleted past the window.
func (p *Purger) PurgeOnce(ctx context.Context) (Report, error) {
	cutoff := p.clock.Now().Add(-p.window)
	var report Report

	groups, err := p.purgeGroups(ctx, cutoff)
	if err != nil {
		return report, fmt.Errorf("purge groups: %w", err)
	}
	report.Groups = groups

	accounts, err := p.purgeAccounts(ctx, cutoff)
	if err != nil {
		return report, fmt.Errorf("purge accounts: %w", err)
	}
	report.Accounts = accounts

	sessions, err := p.purgeSessions(ctx)
	if err != nil {
		return report, fmt.Errorf("purge sessions: %w", err)
	}
	report.Sessions = sessions

	return report, nil
}

// purgeGroups hard-deletes lists soft-deleted before cutoff, with their
// updates, snapshots, log, memberships and invites.
func (p *Purger) purgeGroups(ctx context.Context, cutoff time.Time) (int, error) {
	rows, err := p.pool.Query(ctx, `SELECT id FROM groups WHERE deleted_at IS NOT NULL AND deleted_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0
	for _, id := range ids {
		tx, err := p.pool.Begin(ctx)
		if err != nil {
			return count, err
		}
		ok := true
		for _, stmt := range []string{
			`DELETE FROM list_updates WHERE list_id = $1`,
			`DELETE FROM list_snapshots WHERE list_id = $1`,
			`DELETE FROM join_requests WHERE invite_id IN (SELECT id FROM invites WHERE group_id = $1)`,
			`DELETE FROM invites WHERE group_id = $1`,
			`DELETE FROM group_invites WHERE group_id = $1`,
			`DELETE FROM memberships WHERE group_id = $1`,
			`DELETE FROM group_log WHERE group_id = $1`,
			`DELETE FROM groups WHERE id = $1`,
		} {
			if _, err := tx.Exec(ctx, stmt, id); err != nil {
				slog.Warn("purge group step failed, skipping this group", "group", id, "error", err)
				ok = false
				break
			}
		}
		if !ok {
			_ = tx.Rollback(ctx)
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// purgeAccounts hard-deletes what it safely can for accounts scheduled for
// deletion before cutoff: sessions, unconditionally, then the account row
// itself. It never touches memberships: that table mirrors the signed log,
// and only log entries may change it (deleting an owner's row here would
// leave the list with no owner the validator can find, for good). An
// account still in any group is therefore skipped by the FK below.
//
// Kept simple: an account that authored list_updates or invites can't have
// its row hard-deleted without either breaking other members' ability to
// verify who wrote what, or building a real anonymization strategy — both
// would need a product decision, not this
// job. Those accounts are skipped (logged) and stay invisible via
// deleted_at, which is already true of every API surface that matters.
func (p *Purger) purgeAccounts(ctx context.Context, cutoff time.Time) (int, error) {
	rows, err := p.pool.Query(ctx, `SELECT id FROM accounts WHERE deleted_at IS NOT NULL AND deleted_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0
	for _, id := range ids {
		if _, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE account_id = $1`, id); err != nil {
			return count, err
		}
		// Pending requests from a deleted account can never be approved.
		if _, err := p.pool.Exec(ctx, `DELETE FROM join_requests WHERE account_id = $1 AND approved_at IS NULL`, id); err != nil {
			return count, err
		}
		if _, err := p.pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id); err != nil {
			slog.Warn("purge account skipped: still referenced by history it authored", "account", id, "error", err)
			continue
		}
		count++
	}
	return count, nil
}

// purgeSessions deletes sessions that expired, past their absolute ceiling or
// idle. Other ended sessions are deleted as they end.
func (p *Purger) purgeSessions(ctx context.Context) (int, error) {
	now := p.clock.Now()
	tag, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < $1 OR last_seen_at < $2`,
		now, now.Add(-store.SessionIdleTimeout))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
