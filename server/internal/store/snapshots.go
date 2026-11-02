package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Snapshot struct {
	ListID    string
	Epoch     int64
	CoversSeq int64
	Msg       []byte
	CreatedAt time.Time
}

type Snapshots struct {
	pool *pgxpool.Pool
}

func NewSnapshots(pool *pgxpool.Pool) *Snapshots {
	return &Snapshots{pool: pool}
}

// Upsert stores a snapshot, keeping only the newest per (list, epoch) —
// Sync §7.
func (s *Snapshots) Upsert(ctx context.Context, tx pgx.Tx, listID string, epoch, coversSeq int64, msg []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO list_snapshots (list_id, epoch, covers_seq, msg)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (list_id, epoch) DO UPDATE
		SET covers_seq = GREATEST(list_snapshots.covers_seq, EXCLUDED.covers_seq),
		    msg = CASE WHEN EXCLUDED.covers_seq >= list_snapshots.covers_seq THEN EXCLUDED.msg ELSE list_snapshots.msg END,
		    created_at = now()`,
		listID, epoch, coversSeq, msg)
	if err != nil {
		return fmt.Errorf("upsert snapshot: %w", err)
	}
	return nil
}

// Best returns the snapshot most useful for catch-up from `since`: the one
// with the highest covers_seq that still exceeds it (Sync
// §4), or ErrNotFound if none qualifies.
func (s *Snapshots) Best(ctx context.Context, listID string, since int64) (Snapshot, error) {
	var sn Snapshot
	err := s.pool.QueryRow(ctx, `SELECT list_id, epoch, covers_seq, msg, created_at
		FROM list_snapshots WHERE list_id = $1 AND covers_seq > $2
		ORDER BY covers_seq DESC LIMIT 1`, listID, since,
	).Scan(&sn.ListID, &sn.Epoch, &sn.CoversSeq, &sn.Msg, &sn.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("best snapshot: %w", err)
	}
	return sn, nil
}
