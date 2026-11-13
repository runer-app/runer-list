package e2e_test

import (
	"context"
	"fmt"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"runer-api/internal/testclient"

	"github.com/stretchr/testify/require"
)

// subscribed dials c at instance i and subscribes it to list, through
// catch-up.
func subscribed(t *testing.T, ctx context.Context, c *testclient.Client, i int, list string) *testclient.SyncConn {
	t.Helper()
	pin(c, i)
	defer pin(c, -1)
	conn, err := c.DialSync(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.Sub(ctx, list, 0))
	_, err = conn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)
	return conn
}

// aliceAndBob signs both up behind the cluster and makes bob a member of
// one of alice's lists.
func aliceAndBob(t *testing.T, ctx context.Context, c *cluster) (alice, bob *testclient.Client, l testclient.GroupView) {
	t.Helper()
	alice = newClient(t, c.URL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	bob = newClient(t, c.URL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	l = addMember(t, ctx, alice, createList(t, ctx, alice), bob)
	return alice, bob, l
}

// An update written through one instance reaches subscribers on the other
// live, in seq order, and never comes back to its author.
func TestBehindLoadBalancerLiveDeliveryAcrossInstances(t *testing.T) {
	c := newCluster(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	alice, bob, l := aliceAndBob(t, ctx, c)

	aliceConn := subscribed(t, ctx, alice, 0, l.ID)
	bobConn := subscribed(t, ctx, bob, 1, l.ID)
	bobOnA := subscribed(t, ctx, bob, 0, l.ID) // same instance as the author

	header := alice.UpdateHeader(l.ID, l.Epoch, "")
	const n = 20
	for i := 0; i < n; i++ {
		require.NoError(t, aliceConn.SendUpdate(ctx, fmt.Sprintf("a%d", i), header, fmt.Sprintf("edit-%d", i)))
	}
	for _, conn := range []*testclient.SyncConn{bobConn, bobOnA} {
		var last float64
		for i := 0; i < n; i++ {
			f, err := conn.RecvUntil(ctx, isType("update"))
			require.NoError(t, err, "update %d", i)
			require.Equal(t, fmt.Sprintf("edit-%d", i), f["msg"].(map[string]any)["ct"])
			require.Greater(t, f["seq"].(float64), last, "live updates in seq order")
			last = f["seq"].(float64)
		}
	}
	for i := 0; i < n; i++ {
		f, err := aliceConn.Recv(ctx)
		require.NoError(t, err)
		require.Equal(t, "ack", f["t"], "the author gets acks, never their own update back")
	}

	// And the other way.
	require.NoError(t, bobConn.SendUpdate(ctx, "b", bob.UpdateHeader(l.ID, l.Epoch, ""), "bob-edit"))
	f, err := aliceConn.RecvUntil(ctx, isType("update"))
	require.NoError(t, err)
	require.Equal(t, "bob-edit", f["msg"].(map[string]any)["ct"])
}

// Sync §3, "Ordering per connection": once synced, an ack {seq: X} comes
// after every other author's update with seq < X, also when those authors
// write through another instance at the same time.
func TestBehindLoadBalancerAckComesAfterEarlierUpdates(t *testing.T) {
	c := newCluster(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice, bob, l := aliceAndBob(t, ctx, c)

	const n = 40
	type writer struct {
		client *testclient.Client
		conn   *testclient.SyncConn
	}
	writers := []writer{
		{alice, subscribed(t, ctx, alice, 0, l.ID)},
		{bob, subscribed(t, ctx, bob, 1, l.ID)},
	}
	var wg gosync.WaitGroup
	for wi, w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			header := w.client.UpdateHeader(l.ID, l.Epoch, "")
			go func() {
				for i := 0; i < n; i++ {
					// Writer 1's updates are too large for a notification
					// payload, so instances read them back: slower to
					// deliver than writer 0's own ack would be to send.
					ct := "x"
					if wi == 1 {
						ct = strings.Repeat("x", 10_000)
					}
					if err := w.conn.SendUpdate(ctx, fmt.Sprintf("w%d-%d", wi, i), header, ct); err != nil {
						t.Error(err)
						return
					}
					time.Sleep(time.Millisecond)
				}
			}()
			// Every seq below an ack's is this writer's own (acked
			// earlier) or the other writer's, which must have arrived.
			got := map[int64]bool{}
			acks, updates := 0, 0
			for acks < n || updates < n {
				f, err := w.conn.Recv(ctx)
				if err != nil {
					t.Errorf("writer %d: %v (acks %d, updates %d)", wi, err, acks, updates)
					return
				}
				seq := int64(f["seq"].(float64))
				switch f["t"] {
				case "update":
					updates++
				case "ack":
					acks++
					for s := int64(1); s < seq; s++ {
						if !got[s] {
							t.Errorf("writer %d: ack %d arrived before update %d", wi, seq, s)
							return
						}
					}
				default:
					continue
				}
				got[seq] = true
			}
		}()
	}
	wg.Wait()
}

// A membership change made through one instance sends the `group` frame
// to members connected to the other.
func TestBehindLoadBalancerGroupFrameAcrossInstances(t *testing.T) {
	c := newCluster(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice, bob, l := aliceAndBob(t, ctx, c)
	bobConn := subscribed(t, ctx, bob, 1, l.ID)

	carol := newClient(t, c.URL)
	require.NoError(t, carol.Signup(ctx, []byte("carol-bundle")))
	pin(alice, 0)
	l = addMember(t, ctx, alice, l, carol)
	pin(alice, -1)

	f, err := bobConn.RecvUntil(ctx, func(f map[string]any) bool {
		return f["t"] == "group" && f["group"] == l.ID && f["head_seq"] == float64(l.HeadSeq)
	})
	require.NoError(t, err)
	require.NotNil(t, f)
}

// Forced closes reach the account's sockets on every instance: removal
// from a list, logout, and a rekey (which ends the other sessions).
func TestBehindLoadBalancerForcedClosesAcrossInstances(t *testing.T) {
	c := newCluster(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	expectBye := func(conn *testclient.SyncConn, reason string) {
		t.Helper()
		bye, err := conn.RecvUntil(ctx, isType("bye"))
		require.NoError(t, err)
		require.Equal(t, reason, bye["reason"])
		_, err = conn.RecvUntil(ctx, func(map[string]any) bool { return false })
		require.Error(t, err, "closed after bye")
	}

	t.Run("removal", func(t *testing.T) {
		alice, bob, l := aliceAndBob(t, ctx, c)
		bobConn := subscribed(t, ctx, bob, 1, l.ID)
		pin(alice, 0)
		removeFromGroup(t, ctx, alice, l, bob)
		pin(alice, -1)
		expectBye(bobConn, "remove")
	})

	t.Run("logout", func(t *testing.T) {
		_, bob, l := aliceAndBob(t, ctx, c)
		bobConn := subscribed(t, ctx, bob, 1, l.ID)
		pin(bob, 0)
		require.NoError(t, bob.Logout(ctx))
		expectBye(bobConn, "session-revoked")
	})

	t.Run("rekey", func(t *testing.T) {
		_, bob, l := aliceAndBob(t, ctx, c)
		otherDevice := loginAt(t, ctx, c.URL, bob)
		otherConn := subscribed(t, ctx, otherDevice, 1, l.ID)
		pin(bob, 0)
		require.NoError(t, bob.Rekey(ctx, []byte("bob-bundle-2")))
		expectBye(otherConn, "session-revoked")
	})
}

// If an instance loses its notification connection, notifications sent
// meanwhile are gone: it closes its sockets, so clients reconnect and
// catch up, and it listens again.
func TestBehindLoadBalancerListenerLossClosesSockets(t *testing.T) {
	c := newCluster(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	alice, bob, l := aliceAndBob(t, ctx, c)
	bobConn := subscribed(t, ctx, bob, 1, l.ID)

	var n int
	require.NoError(t, shared.pool.QueryRow(ctx, `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
		WHERE application_name = 'runer-listener'`).Scan(&n))
	require.Positive(t, n, "every instance has a listener connection")

	_, err := bobConn.RecvUntil(ctx, func(map[string]any) bool { return false })
	require.Error(t, err, "the socket is closed")

	// Until it listens again, an instance turns sockets away (503), and
	// clients retry; then live delivery works on new sockets.
	redial := func(c *testclient.Client, i int) *testclient.SyncConn {
		pin(c, i)
		defer pin(c, -1)
		var conn *testclient.SyncConn
		require.Eventually(t, func() bool {
			var err error
			conn, err = c.DialSync(ctx)
			return err == nil
		}, 5*time.Second, 20*time.Millisecond)
		t.Cleanup(func() { _ = conn.Close() })
		require.NoError(t, conn.Sub(ctx, l.ID, 0))
		_, err := conn.RecvUntil(ctx, isType("synced"))
		require.NoError(t, err)
		return conn
	}
	aliceConn := redial(alice, 0)
	bobConn = redial(bob, 1)
	require.NoError(t, aliceConn.SendUpdate(ctx, "a", alice.UpdateHeader(l.ID, l.Epoch, ""), "after"))
	f, err := bobConn.RecvUntil(ctx, isType("update"))
	require.NoError(t, err)
	require.Equal(t, "after", f["msg"].(map[string]any)["ct"])
}

// An update too large for a notification payload is read back from the
// database by the receiving instance.
func TestBehindLoadBalancerLargeUpdateAcrossInstances(t *testing.T) {
	c := newCluster(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice, bob, l := aliceAndBob(t, ctx, c)
	aliceConn := subscribed(t, ctx, alice, 0, l.ID)
	bobConn := subscribed(t, ctx, bob, 1, l.ID)

	big := strings.Repeat("x", 100_000)
	require.NoError(t, aliceConn.SendUpdate(ctx, "big", alice.UpdateHeader(l.ID, l.Epoch, ""), big))
	require.NoError(t, aliceConn.SendUpdate(ctx, "small", alice.UpdateHeader(l.ID, l.Epoch, ""), "small"))
	f, err := bobConn.RecvUntil(ctx, isType("update"))
	require.NoError(t, err)
	require.Equal(t, big, f["msg"].(map[string]any)["ct"])
	f, err = bobConn.RecvUntil(ctx, isType("update"))
	require.NoError(t, err)
	require.Equal(t, "small", f["msg"].(map[string]any)["ct"], "and still in order")
}
