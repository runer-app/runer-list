// Package storetest boots a throwaway PostgreSQL for tests, via
// testcontainers-go. It is imported only from _test.go files, never from
// production code.
package storetest

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// StartPostgres returns a connection string to a ready PostgreSQL database
// that lives until the test ends. See Start.
func StartPostgres(t *testing.T) string {
	t.Helper()
	url, stop, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return url
}

// Start returns a connection string to a ready PostgreSQL database and a
// func that releases it. It reuses TEST_DATABASE_URL if set (CI can point
// this at a shared instance); otherwise it launches an ephemeral container
// via testcontainers. Usable from TestMain, where there is no *testing.T.
func Start(ctx context.Context) (url string, stop func(), err error) {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url, func() {}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("runer"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return "", nil, fmt.Errorf("start postgres container: %w", err)
	}
	stop = func() {
		if err := container.Terminate(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
		}
	}

	url, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("postgres connection string: %w", err)
	}
	return url, stop, nil
}
