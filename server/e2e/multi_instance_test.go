package e2e_test

import (
	"context"
	"fmt"
	"sort"
	gosync "sync"
	"testing"

	"runer-api/internal/auth"
	"runer-api/internal/testclient"

	"github.com/stretchr/testify/require"
)

// secondInstance starts another server over the shared database, as a
// load balancer would put next to the first one.
func secondInstance(t *testing.T) string {
	t.Helper()
	ts := startServer(shared.pool, 0)
	t.Cleanup(ts.Close)
	return ts.URL
}

// loginAt logs c's identity in at another instance, as the same app would
// when the load balancer sends it there.
func loginAt(t *testing.T, ctx context.Context, baseURL string, c *testclient.Client) *testclient.Client {
	t.Helper()
	other := sameIdentityNewInstance(t, baseURL, c.Identity)
	require.NoError(t, other.Login(ctx))
	return other
}

// Writers on two instances update one list at once. Every write is acked
// with its own seq, and a catch-up afterwards returns all of them, 1..N.
func TestTwoInstancesConcurrentWritesKeepSeqsDense(t *testing.T) {
	baseA, _ := newTestServer(t, auth.DefaultChallengeTTL)
	baseB := secondInstance(t)
	ctx := context.Background()

	alice := newClient(t, baseA)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	l := createList(t, ctx, alice)
	aliceB := loginAt(t, ctx, baseB, alice)

	const connsPerInstance, writesPerConn = 3, 30
	var mu gosync.Mutex
	var acked []int64
	var wg gosync.WaitGroup
	for _, c := range []*testclient.Client{alice, aliceB} {
		for k := 0; k < connsPerInstance; k++ {
			conn, err := c.DialSync(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			header := c.UpdateHeader(l.ID, l.Epoch, "")
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < writesPerConn; i++ {
					if err := conn.SendUpdate(ctx, fmt.Sprintf("c%d", i), header, "edit"); err != nil {
						t.Error(err)
						return
					}
				}
				for i := 0; i < writesPerConn; i++ {
					f, err := conn.RecvUntil(ctx, isType("ack"))
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					acked = append(acked, int64(f["seq"].(float64)))
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()

	total := 2 * connsPerInstance * writesPerConn
	sort.Slice(acked, func(i, j int) bool { return acked[i] < acked[j] })
	require.Len(t, acked, total)
	for i, s := range acked {
		require.EqualValues(t, i+1, s, "every write gets its own seq, dense per list")
	}

	reader, err := aliceB.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	require.NoError(t, reader.Sub(ctx, l.ID, 0))
	for i := 1; i <= total; i++ {
		f, err := reader.RecvUntil(ctx, isType("update"))
		require.NoError(t, err)
		require.EqualValues(t, i, f["seq"])
	}
	synced, err := reader.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)
	require.EqualValues(t, total, synced["head"])
}
