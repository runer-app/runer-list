package store_test

import (
	"context"
	"testing"
	"time"

	"runer-api/internal/store"
	"runer-api/internal/store/storetest"

	"github.com/stretchr/testify/require"
)

// The join budget is one bucket per invite, whichever instance spends from
// it: a burst, then a refill at the given rate, capped at the burst.
func TestJoinTokensSharedAcrossInstancesAndRefill(t *testing.T) {
	ctx := context.Background()
	url := storetest.StartPostgres(t)
	a, err := store.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	b, err := store.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(b.Close)

	acct, signPub := seedAccount(t, ctx, a)
	list := seedList(t, ctx, a, acct, signPub)
	_, err = a.Exec(ctx, `INSERT INTO invites (id, group_id, created_by, package, expires_at)
		VALUES ('inv', $1, $2, 'pkg', now() + interval '1 day')`, list, acct)
	require.NoError(t, err)

	take := func(iv *store.Invites, perSecond float64) bool {
		ok, err := iv.TakeJoinToken(ctx, "inv", perSecond, 3)
		require.NoError(t, err)
		return ok
	}
	ia, ib := store.NewInvites(a), store.NewInvites(b)
	const never = 1e-9
	require.True(t, take(ia, never))
	require.True(t, take(ib, never))
	require.True(t, take(ia, never))
	require.False(t, take(ib, never), "the burst is shared by both instances")

	time.Sleep(50 * time.Millisecond)
	require.True(t, take(ia, 100), "refilled at 100/s")
	// 200 ms at 100/s would be 20 tokens, but the bucket holds 3.
	time.Sleep(200 * time.Millisecond)
	require.True(t, take(ib, 100))
	require.True(t, take(ia, never))
	require.True(t, take(ib, never))
	require.False(t, take(ia, never), "never refilled beyond the burst")

	_, err = store.NewInvites(a).TakeJoinToken(ctx, "no-such-invite", 1, 3)
	require.Error(t, err, "only real invites get a budget")
}
