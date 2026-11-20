// Package retention implements the daily purge job, which deletes expired
// sessions. Deleted lists and accounts need no job: they are removed at
// once. The clock is injectable so tests can simulate time passing
// without sleeping.
package retention

import (
	"context"
	"fmt"
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

type Purger struct {
	pool  *pgxpool.Pool
	clock Clock
}

func NewPurger(pool *pgxpool.Pool, clock Clock) *Purger {
	return &Purger{pool: pool, clock: clock}
}

// Report counts what PurgeOnce actually removed, for logging/tests.
type Report struct {
	Sessions int
}

// PurgeOnce runs a single pass. It is safe to call repeatedly (e.g. daily)
// and concurrently with normal request traffic and other instances.
func (p *Purger) PurgeOnce(ctx context.Context) (Report, error) {
	sessions, err := p.purgeSessions(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("purge sessions: %w", err)
	}
	return Report{Sessions: sessions}, nil
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
