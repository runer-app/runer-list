// Package store is the PostgreSQL access layer: connection setup, embedded
// migrations, and queries. It stores only what accounts.go and sessions.go
// declare — never key material, never plaintext content.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a query finds no matching row.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on a unique-constraint violation or a failed
// compare-and-swap (e.g. a stale bundle_gen on rekey).
var ErrConflict = errors.New("conflict")

// ErrAccountHasLists is returned when an account that is still a member of
// a list it doesn't own asks to be deleted.
var ErrAccountHasLists = errors.New("account has lists")

// Access and authorized-write outcomes (see Groups.Access, Groups.AuthorizedWrite).
var (
	ErrForbidden  = errors.New("forbidden")
	ErrStaleEpoch = errors.New("stale epoch")
)

// querier is what both *pgxpool.Pool and pgx.Tx offer for single-row reads,
// so one query can serve a plain read and an in-transaction re-check.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Open connects to PostgreSQL and applies any pending migrations.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return pool, nil
}
