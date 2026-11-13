package store

import (
	"context"
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrateLockKey is the advisory lock held while migrating ("runer-mi").
const migrateLockKey = 0x72756e65722d6d69

// Migrate applies every migration under migrations/ that has not yet run,
// tracked in schema_migrations. Safe to call on every startup, also from
// several instances at once: they take turns under an advisory lock, so
// each migration runs exactly once and the later ones find nothing to do.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return migrateUpTo(ctx, pool, "")
}

// migrateUpTo is Migrate stopping after the file named last ("" for all),
// so a test can build a database as an older server left it.
func migrateUpTo(ctx context.Context, pool *pgxpool.Pool, last string) error {
	// A session lock lives on one connection, so every step runs on it.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(migrateLockKey)); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		// Released with the connection if this fails (the pool closes a
		// broken one), so a failed unlock can't hold up other instances.
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(migrateLockKey))
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if last != "" && name > last {
			break
		}
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}

		sqlBytes, err := migrationFiles.ReadFile(path.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
