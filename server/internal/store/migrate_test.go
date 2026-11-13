package store_test

import (
	"context"
	gosync "sync"
	"testing"

	"runer-api/internal/store"
	"runer-api/internal/store/storetest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Instances started together (a rolling deploy, a scaled-up service) all
// migrate the same empty database at once: each migration runs exactly
// once and every instance starts.
func TestConcurrentMigrateAppliesEachMigrationOnce(t *testing.T) {
	ctx := context.Background()
	url := storetest.StartPostgres(t)

	const instances = 6
	pools := make([]*pgxpool.Pool, instances)
	for i := range pools {
		p, err := pgxpool.New(ctx, url)
		require.NoError(t, err)
		t.Cleanup(p.Close)
		pools[i] = p
	}

	errs := make([]error, instances)
	start := make(chan struct{})
	var wg gosync.WaitGroup
	for i, p := range pools {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = store.Migrate(ctx, p)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "instance %d", i)
	}

	rows, err := pools[0].Query(ctx, `SELECT version, count(*) FROM schema_migrations GROUP BY version`)
	require.NoError(t, err)
	defer rows.Close()
	n := 0
	for rows.Next() {
		var v string
		var c int
		require.NoError(t, rows.Scan(&v, &c))
		require.Equal(t, 1, c, v)
		n++
	}
	require.NoError(t, rows.Err())
	require.Positive(t, n)
}
