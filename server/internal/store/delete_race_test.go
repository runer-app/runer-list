package store_test

import (
	"context"
	"errors"
	gosync "sync"
	"sync/atomic"
	"testing"

	"runer-api/internal/store"
	"runer-api/internal/store/storetest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// A member reading a list's log while its owner deletes it gets either the
// whole log or forbidden, never an empty log: its client would take a log
// shorter than the one it verified for a rollback (E2EE §4.4).
func TestLogReadDuringDeletionIsWholeOrForbidden(t *testing.T) {
	ctx := context.Background()
	pool, err := store.Open(ctx, storetest.StartPostgres(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	groups := store.NewGroups(pool)

	owner, ownerKey := seedAccount(t, ctx, pool)
	member, memberKey := seedAccount(t, ctx, pool)
	ownerID, memberID := uuidString(owner), uuidString(member)

	var empty, whole, forbidden atomic.Int64
	for range 100 {
		list := seedLoggedList(t, ctx, pool, owner, ownerKey, member, memberKey)
		var wg gosync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					entries, err := groups.MemberLog(ctx, list, memberID, 0, 100)
					switch {
					case errors.Is(err, store.ErrForbidden):
						forbidden.Add(1)
						return
					case err != nil:
						t.Error(err)
						return
					case len(entries) == 3:
						whole.Add(1)
					default:
						empty.Add(1)
					}
				}
			}()
		}
		require.NoError(t, groups.Delete(ctx, list, ownerID))
		wg.Wait()
	}
	require.Zero(t, empty.Load(), "a member was served a partial log (%d whole, %d forbidden)", whole.Load(), forbidden.Load())
	require.Positive(t, whole.Load())
}

// seedLoggedList is a list of owner's with member in it and a three-entry log.
func seedLoggedList(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, ownerKey, member, memberKey []byte) string {
	t.Helper()
	list := seedList(t, ctx, pool, owner, ownerKey)
	_, err := pool.Exec(ctx, `INSERT INTO memberships (group_id, account_id, identity_sign_pub, role) VALUES ($1, $2, $3, 'member')`,
		list, member, memberKey)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO group_log (group_id, seq, entry, sig, hash, ts)
		SELECT $1, s, 'e', 's', 'h', now() FROM generate_series(0, 2) AS s`, list)
	require.NoError(t, err)
	return list
}
