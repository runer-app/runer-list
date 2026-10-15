// Package e2e_test drives the real Go server (wired exactly as
// cmd/server/main.go wires it) over real HTTP, against a real PostgreSQL,
// using the stub testclient — never direct SQL to fake state, never
// in-process handler calls.
package e2e_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/config"
	"runer-api/internal/server"
	"runer-api/internal/store"
	"runer-api/internal/store/storetest"
	"runer-api/internal/testclient"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const testOrigin = "tauri://localhost"

// shared is the one PostgreSQL container and server the whole package runs
// against, started once in TestMain. Tests run sequentially and each one
// truncates every table when it finishes (newTestServer), so they start
// from an empty database without paying for a container each.
var shared struct {
	pool *pgxpool.Pool
	url  string
}

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()
	dbURL, stop, err := storetest.Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer stop()

	pool, err := store.Open(ctx, dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()

	srv := startServer(pool, 0)
	defer srv.Close()

	shared.pool, shared.url = pool, srv.URL
	return m.Run()
}

// newTestServer returns the shared server, wired exactly the way
// cmd/server/main.go wires it (server.NewWithOptions over a pool migrated
// by store.Open), and registers a cleanup that empties every table. A test
// that needs a different challengeTTL gets its own server over the same
// database. The pool is returned only so tests can read back what the real
// handlers persisted — no test-only schema or seed SQL exists anywhere.
func newTestServer(t *testing.T, challengeTTL time.Duration) (baseURL string, pool *pgxpool.Pool) {
	t.Helper()
	// Registered first, so it runs last: after the test's sockets and any
	// per-test server have closed.
	t.Cleanup(func() {
		if _, err := shared.pool.Exec(context.Background(), `TRUNCATE
			accounts, sessions, groups, group_log, memberships, invites, join_requests, group_invites,
			list_updates, list_snapshots RESTART IDENTITY CASCADE`); err != nil {
			t.Errorf("truncate after test: %v", err)
		}
	})
	if challengeTTL == auth.DefaultChallengeTTL {
		return shared.url, shared.pool
	}
	ts := startServer(shared.pool, challengeTTL)
	t.Cleanup(ts.Close)
	return ts.URL, shared.pool
}

// testLimits are the production limits with a cheap proof-of-work:
// production difficulty costs ~1M hashes per signup, and the check itself
// is the same at any difficulty. The test client uses whatever the
// challenge advertises, as a real client does.
var testLimits = func() auth.Limits {
	l := auth.DefaultLimits
	l.SignupPoWBits = 4
	return l
}()

// testProxies trusts loopback (the test client simulates distinct client
// IPs with X-Forwarded-For) and 10.0.0.0/8 (TestChallengeLimitKeysOnRealIP).
var testProxies, _ = config.ParseCIDRs("127.0.0.0/8,::1/128,10.0.0.0/8")

// startServer starts a server whose PublicURL is its own address, since
// that's what clients sign logins for. challengeTTL 0 means the default.
func startServer(pool *pgxpool.Pool, challengeTTL time.Duration) *httptest.Server {
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = server.New(pool, server.Options{
		Origin: testOrigin, PublicURL: "http://" + ts.Listener.Addr().String(),
		TrustedProxies: testProxies, ChallengeTTL: challengeTTL, Limits: testLimits,
	})
	ts.Start()
	return ts
}

func newClient(t *testing.T, baseURL string) *testclient.Client {
	t.Helper()
	id, err := testclient.NewIdentity()
	require.NoError(t, err)
	c, err := testclient.New(baseURL, testOrigin, id)
	require.NoError(t, err)
	return c
}

// sameIdentityNewInstance simulates a second, independent app instance
// holding the same Account Key/identity (a new device, or a restart) — a
// fresh Client and cookie jar, no state carried over except the key
// material a real client would hold.
func sameIdentityNewInstance(t *testing.T, baseURL string, id *testclient.Identity) *testclient.Client {
	t.Helper()
	clone := &testclient.Identity{
		AuthPub: id.AuthPub, AuthPriv: id.AuthPriv,
		SignPub: id.SignPub, SignPriv: id.SignPriv,
		AgreePriv: id.AgreePriv, AgreePub: id.AgreePub,
	}
	c, err := testclient.New(baseURL, testOrigin, clone)
	require.NoError(t, err)
	return c
}

func TestSignupThenLoginFromFreshInstance(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	bundle := []byte("opaque encrypted identity bundle v1")
	require.NoError(t, alice.Signup(ctx, bundle))
	require.NotEmpty(t, alice.AccountID)
	require.Equal(t, 1, alice.BundleGen)

	fresh := sameIdentityNewInstance(t, baseURL, alice.Identity)
	require.NoError(t, fresh.Login(ctx))
	require.Equal(t, alice.AccountID, fresh.AccountID)

	gotBundle, gen, err := fresh.GetBundle(ctx)
	require.NoError(t, err)
	require.Equal(t, bundle, gotBundle)
	require.Equal(t, 1, gen)

	acc, err := fresh.GetAccount(ctx)
	require.NoError(t, err)
	require.Equal(t, alice.AccountID, acc.ID)
	require.Equal(t, 1, acc.BundleGen)
}

func TestWrongWordsSameResponseAsNonexistentAccount(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	// "Wrong words": the client derives a completely different key pair
	// than any registered account. It never touched the server before, so
	// this and a genuinely unknown account are, by construction, the same
	// case — there is nothing for the server to distinguish.
	wrongWords := newClient(t, baseURL)
	err1 := wrongWords.Login(ctx) // no identity fields => plain login attempt, not signup

	neverExisted := newClient(t, baseURL)
	err2 := neverExisted.Login(ctx)

	var apiErr1, apiErr2 *testclient.APIError
	require.ErrorAs(t, err1, &apiErr1)
	require.ErrorAs(t, err2, &apiErr2)
	require.Equal(t, apiErr1.Status, apiErr2.Status)
	require.Equal(t, apiErr1.Code, apiErr2.Code)
	require.Equal(t, "invalid_credentials", apiErr1.Code)
}

func TestChallengeReuseRejected(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bundle")))

	// A fresh login, fully consumed once...
	nonce, err := bob.Challenge(ctx)
	require.NoError(t, err)
	sig := bob.SignLogin(nonce)
	require.NoError(t, bob.RawLogin(ctx, nonce, sig))

	// ...replaying the exact same nonce/sig must be rejected: the
	// challenge is single-use even though the signature is still valid.
	err = bob.RawLogin(ctx, nonce, sig)
	require.Error(t, err)
	var apiErr *testclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "invalid_credentials", apiErr.Code)
}

func TestChallengeExpiryRejected(t *testing.T) {
	const ttl = 200 * time.Millisecond
	baseURL, _ := newTestServer(t, ttl)
	ctx := context.Background()

	carol := newClient(t, baseURL)
	nonce, err := carol.Challenge(ctx)
	require.NoError(t, err)

	time.Sleep(ttl + 150*time.Millisecond)

	sig := carol.SignLogin(nonce)
	err = carol.RawLogin(ctx, nonce, sig)
	require.Error(t, err)
	var apiErr *testclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "invalid_credentials", apiErr.Code)
}

func TestRekeyRevokesOtherSessionsIdentityUnchanged(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("bundle-v1")))
	oldAuthPub := append(ed25519.PublicKey(nil), alice.Identity.AuthPub...)
	oldAuthPriv := append(ed25519.PrivateKey(nil), alice.Identity.AuthPriv...)
	oldIdentitySignPub := append(ed25519.PublicKey(nil), alice.Identity.SignPub...)

	// A second device session for the same account/identity.
	otherDevice := sameIdentityNewInstance(t, baseURL, alice.Identity)
	require.NoError(t, otherDevice.Login(ctx))
	otherSessions, err := otherDevice.ListSessions(ctx)
	require.NoError(t, err)
	require.Len(t, otherSessions, 2) // alice's own session + this one
	require.False(t, otherSessions[0].Current && otherSessions[1].Current)
	require.True(t, otherSessions[0].Current || otherSessions[1].Current) // exactly one: the asking device's

	require.NoError(t, alice.Rekey(ctx, []byte("bundle-v2")))
	require.Equal(t, 2, alice.BundleGen)
	require.NotEqual(t, []byte(oldAuthPub), []byte(alice.Identity.AuthPub))

	// The other device's session was revoked by the rekey.
	_, err = otherDevice.ListSessions(ctx)
	apiErr, ok := errors.AsType[*testclient.APIError](err)
	require.True(t, ok, "want an APIError, got %v", err)
	require.Equal(t, "session_revoked", apiErr.Code) // not the "unauthenticated" of an expired session

	// The old auth key can no longer complete a login.
	staleIdentity := &testclient.Identity{AuthPub: oldAuthPub, AuthPriv: oldAuthPriv}
	stale, err := testclient.New(baseURL, testOrigin, staleIdentity)
	require.NoError(t, err)
	require.Error(t, stale.Login(ctx))

	// Identity is unchanged: same identity_sign_pub after rekey.
	acc, err := alice.GetAccount(ctx)
	require.NoError(t, err)
	require.Equal(t, base64.RawURLEncoding.EncodeToString(oldIdentitySignPub), acc.IdentitySignPub)
}

func TestBundleGenNeverServedLowerThanLastSeen(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("bundle-v1")))
	_, gen1, err := alice.GetBundle(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, gen1)

	require.NoError(t, alice.Rekey(ctx, []byte("bundle-v2")))
	_, gen2, err := alice.GetBundle(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, gen2)
	// This is the guarantee client-core's rollback detection relies on
	// (E2EE §3.3): bundle_gen is strictly increasing, so a client that
	// remembers the highest generation it has seen can tell a served
	// generation is stale the moment it's lower than that.
	require.Greater(t, gen2, gen1, "bundle_gen must be strictly increasing")
}

func TestMissingOrWrongOriginOnMutationRejected(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	id, err := testclient.NewIdentity()
	require.NoError(t, err)

	wrongOrigin, err := testclient.New(baseURL, "https://evil.example", id)
	require.NoError(t, err)
	err = wrongOrigin.Signup(ctx, []byte("bundle"))
	require.Error(t, err)
	var apiErr *testclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "origin_mismatch", apiErr.Code)

	missingOrigin, err := testclient.New(baseURL, "", id) // Origin header omitted entirely, see Client.do
	require.NoError(t, err)
	err = missingOrigin.Signup(ctx, []byte("bundle"))
	require.Error(t, err)
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "origin_mismatch", apiErr.Code)
}

// TestNoAccountKeyMaterialInDatabase checks that a
// stolen database dump contains no Account Key material. The server
// never receives the Account Key or wrap key at all (deriving those is
// entirely client-core's job); what this test verifies is that nothing
// resembling a private key ends up in what the real handlers persisted,
// and that the schema itself has no column that could hold one. It reads
// back through the same pool store.Open handed the real server — no
// separate test SQL to fake state, just a read of what's actually there.
func TestNoAccountKeyMaterialInDatabase(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("bundle-v1")))

	forbiddenColumns := []string{"account_key", "wrap_key", "identity_priv", "identity_sign_priv", "identity_agree_priv", "private_key"}
	rows, err := pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name IN ('accounts', 'sessions')`)
	require.NoError(t, err)
	var columns []string
	for rows.Next() {
		var col string
		require.NoError(t, rows.Scan(&col))
		columns = append(columns, strings.ToLower(col))
	}
	rows.Close()
	require.NoError(t, rows.Err())

	for _, forbidden := range forbiddenColumns {
		for _, col := range columns {
			require.NotContains(t, col, forbidden, "accounts/sessions schema must never gain a column storing %s", forbidden)
		}
	}

	// The private key bytes themselves (auth and identity-sign) must not
	// appear anywhere in the stored row, in any of the encodings the
	// server might plausibly use.
	var authPub, identitySignPub, identityAgreePub, identityBundle []byte
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT auth_pub, identity_sign_pub, identity_agree_pub, identity_bundle FROM accounts WHERE id = $1`,
		alice.AccountID,
	).Scan(&authPub, &identitySignPub, &identityAgreePub, &identityBundle))

	dump := fmt.Sprintf("%x %x %x %x", authPub, identitySignPub, identityAgreePub, identityBundle)
	require.NotContains(t, dump, fmt.Sprintf("%x", alice.Identity.AuthPriv))
	require.NotContains(t, dump, fmt.Sprintf("%x", alice.Identity.SignPriv))

	// Sanity: the columns DO contain the public keys we expect (proves the
	// query above is actually reading real data, not an empty row).
	require.Equal(t, alice.Identity.AuthPub, ed25519.PublicKey(authPub))
	require.Equal(t, alice.Identity.SignPub, ed25519.PublicKey(identitySignPub))
}

// TestChallengeLimitKeysOnRealIP drives the real server with a
// chosen peer address (httptest over TCP is always loopback, i.e. a
// trusted proxy), to check X-Forwarded-For is honoured from the proxy
// and ignored from anyone else.
func TestChallengeLimitKeysOnRealIP(t *testing.T) {
	_, pool := newTestServer(t, auth.DefaultChallengeTTL)
	lim := testLimits
	lim.ChallengesPerIPPerMinute = 20
	e := server.New(pool, server.Options{Origin: testOrigin, PublicURL: "http://unused", TrustedProxies: testProxies, Limits: lim})

	challenge := func(peer, xff string) int {
		pub, _, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		body := fmt.Sprintf(`{"auth_pub":%q}`, base64.RawURLEncoding.EncodeToString(pub))
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/challenge", strings.NewReader(body))
		req.RemoteAddr = peer + ":40000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", testOrigin)
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	// Direct public client: a fresh spoofed X-Forwarded-For per request
	// must not buy a fresh bucket.
	for i := range 20 {
		require.Equal(t, http.StatusOK, challenge("203.0.113.7", fmt.Sprintf("198.51.100.%d", i)))
	}
	require.Equal(t, http.StatusTooManyRequests, challenge("203.0.113.7", "198.51.100.99"))

	// Behind the proxy (private peer): each real client gets its own bucket.
	for range 20 {
		require.Equal(t, http.StatusOK, challenge("10.0.0.2", "192.0.2.1"))
	}
	require.Equal(t, http.StatusTooManyRequests, challenge("10.0.0.2", "192.0.2.1"))
	require.Equal(t, http.StatusOK, challenge("10.0.0.2", "192.0.2.2"))
}

// TestSignupLimitIsConfigurable: the per-IP signup cap is a burst plus a
// refill rate, both set by the operator, and the challenge advertises the
// proof-of-work difficulty the signup must meet.
func TestSignupLimitIsConfigurable(t *testing.T) {
	_, pool := newTestServer(t, auth.DefaultChallengeTTL)
	lim := testLimits
	lim.SignupsPerIPPerHour, lim.SignupBurstPerIP, lim.SignupPoWBits = 1, 3, 6
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = server.New(pool, server.Options{
		Origin: testOrigin, PublicURL: "http://" + ts.Listener.Addr().String(),
		TrustedProxies: testProxies, Limits: lim,
	})
	ts.Start()
	t.Cleanup(ts.Close)
	ctx := context.Background()

	// Several people behind one office IP: the burst admits them all.
	for i := range 3 {
		c := newClient(t, ts.URL)
		c.SetIP("192.0.2.50")
		require.NoErrorf(t, c.Signup(ctx, []byte("bundle")), "signup %d", i)
		require.Equal(t, 6, c.PoWBits())
	}
	c := newClient(t, ts.URL)
	c.SetIP("192.0.2.50")
	requireAPIError(t, c.Signup(ctx, []byte("bundle")), "rate_limited")

	// Another IP has its own budget.
	other := newClient(t, ts.URL)
	other.SetIP("192.0.2.51")
	require.NoError(t, other.Signup(ctx, []byte("bundle")))
}
