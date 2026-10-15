package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Update struct {
	ListID          string
	Seq             int64
	Epoch           int64
	AuthorAccountID string
	Msg             []byte
	CreatedAt       time.Time
}

type Updates struct {
	pool *pgxpool.Pool
}

func NewUpdates(pool *pgxpool.Pool) *Updates {
	return &Updates{pool: pool}
}

// Append persists one encrypted update and assigns it the next seq (Sync
// Protocol & API §3, §8: server-assigned, monotonic). Runs inside
// Groups.AuthorizedWrite's transaction.
func (u *Updates) Append(ctx context.Context, tx pgx.Tx, listID string, epoch int64, authorAccountID string, msg []byte) (Update, error) {
	var up Update
	err := tx.QueryRow(ctx, `INSERT INTO list_updates (list_id, epoch, author_account_id, msg)
		VALUES ($1, $2, $3, $4)
		RETURNING list_id, seq, epoch, author_account_id, msg, created_at`,
		listID, epoch, authorAccountID, msg,
	).Scan(&up.ListID, &up.Seq, &up.Epoch, &up.AuthorAccountID, &up.Msg, &up.CreatedAt)
	if err != nil {
		return Update{}, fmt.Errorf("append update: %w", err)
	}
	return up, nil
}

// ListSince returns up to limit updates with seq > sinceSeq, in order — one
// page of the catch-up tail (Sync §4). Callers loop, so a
// long history is never loaded into memory at once.
func (u *Updates) ListSince(ctx context.Context, listID string, sinceSeq int64, limit int) ([]Update, error) {
	rows, err := u.pool.Query(ctx, `SELECT list_id, seq, epoch, author_account_id, msg, created_at
		FROM list_updates WHERE list_id = $1 AND seq > $2 ORDER BY seq LIMIT $3`,
		listID, sinceSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("list updates: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Update, error) {
		var up Update
		err := row.Scan(&up.ListID, &up.Seq, &up.Epoch, &up.AuthorAccountID, &up.Msg, &up.CreatedAt)
		return up, err
	})
}

// HeadSeq returns the highest seq stored for this list, or 0 if none.
func (u *Updates) HeadSeq(ctx context.Context, tx pgx.Tx, listID string) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM list_updates WHERE list_id = $1`, listID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("list head seq: %w", err)
	}
	return seq, nil
}
