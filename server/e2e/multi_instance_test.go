package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/testclient"

	"github.com/stretchr/testify/require"
)

// cluster is n server instances over the shared database behind a
// round-robin load balancer, the shape docs/self-hosting.md describes.
// Every request (and every WebSocket) goes to the next instance in turn,
// so a client's consecutive requests always land on different instances.
type cluster struct {
	URL string // the load balancer's: every instance's PublicURL
}

// pinHeader steers a request to one instance (0-based) instead of the next.
const pinHeader = "X-Test-Instance"

// pin sends c's requests and dials to instance i; -1 goes back to round robin.
func pin(c *testclient.Client, i int) {
	if i < 0 {
		c.Header = nil
		return
	}
	c.Header = http.Header{pinHeader: []string{strconv.Itoa(i)}}
}

func newCluster(t *testing.T, n int) cluster {
	t.Helper()
	newTestServer(t, auth.DefaultChallengeTTL) // the per-test cleanup
	lb := httptest.NewUnstartedServer(nil)
	publicURL := "http://" + lb.Listener.Addr().String()
	proxies := make([]*httputil.ReverseProxy, n)
	for i := range proxies {
		ts := startServerAt(shared.pool, 0, publicURL)
		t.Cleanup(ts.Close)
		u, err := url.Parse(ts.URL)
		require.NoError(t, err)
		proxies[i] = httputil.NewSingleHostReverseProxy(u)
	}
	var next atomic.Uint64
	lb.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pin := r.Header.Get(pinHeader); pin != "" {
			i, err := strconv.Atoi(pin)
			if err != nil || i < 0 || i >= n {
				http.Error(w, "bad "+pinHeader, http.StatusBadRequest)
				return
			}
			proxies[i].ServeHTTP(w, r)
			return
		}
		proxies[(next.Add(1)-1)%uint64(n)].ServeHTTP(w, r)
	})
	lb.Start()
	t.Cleanup(lb.Close)
	return cluster{URL: lb.URL}
}

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

// A challenge issued by one instance is answered at the other: signup,
// login and rekey all work behind the load balancer.
func TestBehindLoadBalancerChallengesWorkOnEveryInstance(t *testing.T) {
	c := newCluster(t, 2)
	ctx := context.Background()

	alice := newClient(t, c.URL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	for i := 0; i < 3; i++ {
		again := sameIdentityNewInstance(t, c.URL, alice.Identity)
		require.NoError(t, again.Login(ctx), "login %d", i)
	}
	require.NoError(t, alice.Rekey(ctx, []byte("alice-bundle-2")))
	again := sameIdentityNewInstance(t, c.URL, alice.Identity)
	require.NoError(t, again.Login(ctx), "login with the new auth key")
}

// A challenge is still single use when the second answer goes to the
// other instance.
func TestBehindLoadBalancerChallengeSingleUse(t *testing.T) {
	c := newCluster(t, 2)
	ctx := context.Background()

	alice := newClient(t, c.URL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	nonce, err := alice.Challenge(ctx)
	require.NoError(t, err)
	sig := alice.SignLogin(nonce)
	require.NoError(t, alice.RawLogin(ctx, nonce, sig))
	require.Error(t, alice.RawLogin(ctx, nonce, sig), "replayed at the other instance")
}

// The join limit guards an invite, a credential: 10 attempts (then 10 a
// day) in all, however the load balancer spreads them.
func TestBehindLoadBalancerJoinLimitIsPerInvite(t *testing.T) {
	c := newCluster(t, 2)
	ctx := context.Background()

	alice := newClient(t, c.URL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	l := createList(t, ctx, alice)
	_, inviteID, mac := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, l.ID, inviteID, []byte("pkg"), time.Now().Add(24*time.Hour))
	require.NoError(t, err)

	bob := newClient(t, c.URL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	joinMAC := mac([]byte(inviteID + b64pub(bob)))
	for i := 0; i < 10; i++ {
		_, err := bob.JoinInvite(ctx, inviteID, []byte("name"), joinMAC)
		require.NoError(t, err, "attempt %d", i+1)
	}
	_, err = bob.JoinInvite(ctx, inviteID, []byte("name"), joinMAC)
	var apiErr *testclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 429, apiErr.Status)
}
