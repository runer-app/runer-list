package e2e_test

// Regression tests for security hardening: limits, session end, ordering,
// atomicity, deletion and retention. Each test names what it protects.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/retention"
	"runer-api/internal/server"
	"runer-api/internal/store"
	"runer-api/internal/testclient"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// A far-future ts must be rejected, on create and on append.
func TestFutureTimestampRejected(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))

	future := time.Now().Add(365 * 24 * time.Hour).UnixMilli()
	_, err := alice.CreateGroup(ctx, testclient.GroupEntry{V: 1, Type: "create", Author: b64pub(alice), Ts: future})
	requireAPIError(t, err, "bad_request")

	ws := createList(t, ctx, alice)
	rotate := testclient.GroupEntry{
		V: 1, Group: ws.ID, Seq: ws.HeadSeq + 1, Prev: ws.HeadHash, Type: "rotate",
		Author: b64pub(alice), Epoch: ws.Epoch + 1,
		Envelopes: []testclient.GroupEnvelope{{Recipient: b64pub(alice), HPKECiphertext: "Y3Q"}},
		KeyChain:  "a2V5Y2hhaW4", Ts: future,
	}
	_, err = alice.AppendLog(ctx, ws.ID, rotate)
	requireAPIError(t, err, "bad_request")

	// Small clock skew is still fine.
	rotate.Ts = time.Now().Add(time.Minute).UnixMilli()
	_, err = alice.AppendLog(ctx, ws.ID, rotate)
	require.NoError(t, err)
}

// Account purge must not change memberships behind the log's back.
func TestPurgeKeepsMemberships(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	ws := createList(t, ctx, alice)
	// Through the API an account's lists are deleted with it, so mark it
	// deleted directly: the purge must be safe on its own.
	_, err := pool.Exec(ctx, `UPDATE accounts SET deleted_at = now() WHERE id = $1`, alice.AccountID)
	require.NoError(t, err)

	purger := retention.NewPurger(pool, fakeClock{now: time.Now().Add(retention.DefaultWindow + time.Hour)}, retention.DefaultWindow)
	_, err = purger.PurgeOnce(ctx)
	require.NoError(t, err)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE group_id = $1 AND role = 'owner'`, ws.ID).Scan(&n))
	require.Equal(t, 1, n, "the owner's membership must survive purge")
}

// Oversized bodies are refused before they are parsed.
func TestBodyLimit(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)

	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/auth/login", bytes.NewReader(make([]byte, 2<<20)))
	require.NoError(t, err)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

// Identity_agree_pub must be a 32-byte X25519 key at signup.
func TestAgreePubLengthChecked(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	bad := newClient(t, baseURL)
	bad.Identity.AgreePub = make([]byte, 1000)
	require.Error(t, bad.Signup(ctx, []byte("b")))
}

// ---- sync helpers ----

func dialSynced(t *testing.T, ctx context.Context, c *testclient.Client, list string) *testclient.SyncConn {
	t.Helper()
	conn, err := c.DialSync(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.Sub(ctx, list, 0))
	_, err = conn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)
	return conn
}

// requireBye waits for a `bye` with reason, then for the socket to close.
func requireBye(t *testing.T, ctx context.Context, conn *testclient.SyncConn, reason string) {
	t.Helper()
	bye, err := conn.RecvUntil(ctx, isType("bye"))
	require.NoError(t, err)
	require.Equal(t, reason, bye["reason"])
	_, err = conn.RecvUntil(ctx, func(map[string]any) bool { return false })
	require.Error(t, err, "connection must be closed after bye")
}

func requireStillOpen(t *testing.T, ctx context.Context, conn *testclient.SyncConn, list string) {
	t.Helper()
	require.NoError(t, conn.Sub(ctx, list, 0))
	_, err := conn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)
}

// waitForLockWait returns once some backend is blocked on a lock, i.e. the
// request under test has reached the transaction the test holds open.
func waitForLockWait(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	require.Eventually(t, func() bool {
		var n int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&n)
		return err == nil && n > 0
	}, 5*time.Second, 5*time.Millisecond)
}

func isErrorCode(code string) func(map[string]any) bool {
	return func(f map[string]any) bool { return f["t"] == "error" && f["code"] == code }
}

func isAck(cid string) func(map[string]any) bool {
	return func(f map[string]any) bool { return f["t"] == "ack" && f["cid"] == cid }
}

// twoSessions signs alice up and logs the same identity in a second time,
// returning both clients (second is the newest session).
func twoSessions(t *testing.T, ctx context.Context, baseURL string) (*testclient.Client, *testclient.Client) {
	t.Helper()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	alice2 := sameIdentityNewInstance(t, baseURL, alice.Identity)
	require.NoError(t, alice2.Login(ctx))
	return alice, alice2
}

// ---- Session end closes open sockets ----

func TestLogoutClosesOnlyThatSessionsSocket(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, alice2 := twoSessions(t, ctx, baseURL)
	ws := createList(t, ctx, alice)

	conn1 := dialSynced(t, ctx, alice, ws.ID)
	conn2 := dialSynced(t, ctx, alice2, ws.ID)

	require.NoError(t, alice2.Logout(ctx))

	requireBye(t, ctx, conn2, "session-revoked")
	requireStillOpen(t, ctx, conn1, ws.ID)
}

func TestLogoutClosesSocket(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	ws := createList(t, ctx, alice)

	conn := dialSynced(t, ctx, alice, ws.ID)
	require.NoError(t, alice.Logout(ctx))
	requireBye(t, ctx, conn, "session-revoked")
}

func TestRekeyClosesOtherSessionsSockets(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, alice2 := twoSessions(t, ctx, baseURL)
	ws := createList(t, ctx, alice)

	conn1 := dialSynced(t, ctx, alice, ws.ID)
	conn2 := dialSynced(t, ctx, alice2, ws.ID)

	require.NoError(t, alice.Rekey(ctx, []byte("new-bundle")))
	requireBye(t, ctx, conn2, "session-revoked")
	requireStillOpen(t, ctx, conn1, ws.ID)
}

func TestAccountDeletionClosesSockets(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	ws := createList(t, ctx, alice)

	conn := dialSynced(t, ctx, alice, ws.ID)
	require.NoError(t, alice.DeleteAccount(ctx))
	requireBye(t, ctx, conn, "session-revoked")
}

// The database, not the hub notification, is the authority: a session
// ended where no notification reaches this process (e.g. another server
// instance) still stops the socket at its next frame.
func TestSessionCheckedOnEveryFrame(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	ws := createList(t, ctx, alice)
	conn := dialSynced(t, ctx, alice, ws.ID)

	// Direct SQL stands in for the missed notification; there's no API that
	// ends a session without also notifying.
	_, err := pool.Exec(ctx, `DELETE FROM sessions`)
	require.NoError(t, err)

	require.NoError(t, conn.SendUpdate(ctx, "c1", alice.UpdateHeader(ws.ID, ws.Epoch, ""), "x"))
	requireBye(t, ctx, conn, "session-revoked")
}

// ---- A subscriber that stops reading can't stall anyone else ----

func TestSlowSubscriberDoesNotStallOthers(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice, bob, carol := newClient(t, baseURL), newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	require.NoError(t, carol.Signup(ctx, []byte("c")))
	ws := createList(t, ctx, alice)
	ws = addMember(t, ctx, alice, ws, bob)
	ws = addMember(t, ctx, alice, ws, carol)

	const n = 12
	_ = dialSynced(t, ctx, carol, ws.ID) // carol never reads again
	bobConn := dialSynced(t, ctx, bob, ws.ID)
	aliceConn := dialSynced(t, ctx, alice, ws.ID)

	// ~5 MB of updates: more than carol's socket buffers hold, so the
	// old relay blocked alice's handler on carol's socket.
	ct := string(bytes.Repeat([]byte("x"), 400*1024))
	header := alice.UpdateHeader(ws.ID, ws.Epoch, "")
	for i := range n {
		cid := "u" + strconv.Itoa(i)
		require.NoError(t, aliceConn.SendUpdate(ctx, cid, header, ct))
		_, err := aliceConn.RecvUntil(ctx, isAck(cid))
		require.NoError(t, err, "publisher stalled at update %d", i)
	}
	for i := range n {
		_, err := bobConn.RecvUntil(ctx, isType("update"))
		require.NoError(t, err, "healthy subscriber stalled at update %d", i)
	}
}

// ---- Catch-up is paged and complete ----

func TestCatchUpLongerThanOneBatch(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	ws := createList(t, ctx, alice)

	// Seed the history directly: going through the relay would take 10 s
	// at 50 updates/s, and what's under test here is catch-up, not writes.
	const n = 520 // > one catch-up batch (500)
	_, err := pool.Exec(ctx, `INSERT INTO list_updates (list_id, epoch, author_account_id, msg)
		SELECT $1, 0, $2, convert_to(json_build_object('ct', 'u' || (i - 1))::text, 'UTF8')
		FROM generate_series(1, $3::int) AS i ORDER BY i`, ws.ID, alice.AccountID, n)
	require.NoError(t, err)

	reader, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	require.NoError(t, reader.Sub(ctx, ws.ID, 0))

	var last float64
	for i := range n {
		f, err := reader.RecvUntil(ctx, isType("update"))
		require.NoError(t, err)
		require.Equal(t, "u"+strconv.Itoa(i), f["msg"].(map[string]any)["ct"], "catch-up out of order or incomplete")
		last = f["seq"].(float64)
	}
	synced, err := reader.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)
	require.Equal(t, last, synced["head"])
}

// ---- Sub frames are budgeted ----

func TestSubFramesRateLimited(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	ws := createList(t, ctx, alice)

	conn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	for range 30 {
		require.NoError(t, conn.Sub(ctx, ws.ID, 0))
	}
	_, err = conn.RecvUntil(ctx, isErrorCode("rate-limited"))
	require.NoError(t, err)
}

func TestInvitePackageReadsRateLimited(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	anon := newClient(t, baseURL)

	var err error
	for range 31 {
		_, err = anon.GetInvitePackage(ctx, "no-such-invite")
	}
	requireAPIError(t, err, "rate_limited")
}

// ---- Authorization and write are one linearized step ----

// A write that arrives while a membership change holds the group row waits
// for it, then is judged against the committed result.
func TestWriteSerializedWithEpochChange(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	ws := createList(t, ctx, alice)
	conn := dialSynced(t, ctx, alice, ws.ID)

	// Stands in for a rotation's append transaction, paused before commit.
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `UPDATE groups SET epoch = epoch + 1 WHERE id = $1`, ws.ID)
	require.NoError(t, err)

	require.NoError(t, conn.SendUpdate(ctx, "c1", alice.UpdateHeader(ws.ID, ws.Epoch, ""), "old-epoch"))
	waitForLockWait(t, ctx, pool)
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM list_updates`).Scan(&n))
	require.Zero(t, n, "write must wait for the in-flight membership change")

	require.NoError(t, tx.Commit(ctx))
	_, err = conn.RecvUntil(ctx, isErrorCode("stale-epoch"))
	require.NoError(t, err)
}

// ---- Deletion and retention ----

func TestListDeletionEndsAccessAndPurgesContent(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	l := createList(t, ctx, alice)

	conn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SendUpdate(ctx, "c1", alice.UpdateHeader(l.ID, l.Epoch, ""), "x"))
	_, err = conn.RecvUntil(ctx, isAck("c1"))
	require.NoError(t, err)
	require.NoError(t, conn.SendSnapshot(ctx, "s1", alice.UpdateHeader(l.ID, l.Epoch, ""), 1, "snap"))
	_, err = conn.RecvUntil(ctx, isAck("s1"))
	require.NoError(t, err)

	require.NoError(t, alice.DeleteGroup(ctx, l.ID))

	_, err = alice.GetLog(ctx, l.ID)
	requireAPIError(t, err, "forbidden")
	require.NoError(t, conn.SendUpdate(ctx, "c2", alice.UpdateHeader(l.ID, l.Epoch, ""), "x"))
	_, err = conn.RecvUntil(ctx, isErrorCode("forbidden"))
	require.NoError(t, err)
	lists := sharedLists(t, ctx, alice)
	require.Empty(t, lists)

	// After the window the list and all its content are gone.
	purger := retention.NewPurger(pool, fakeClock{now: time.Now().Add(retention.DefaultWindow + time.Hour)}, retention.DefaultWindow)
	report, err := purger.PurgeOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, report.Groups)
	for _, q := range []string{
		`SELECT count(*) FROM groups WHERE id NOT IN (SELECT inbox_id FROM accounts)`,
		`SELECT count(*) FROM list_updates`, `SELECT count(*) FROM list_snapshots`,
	} {
		var n int
		require.NoError(t, pool.QueryRow(ctx, q).Scan(&n))
		require.Zero(t, n, q)
	}
}

func TestAccountDeletionClearsBundle(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	require.NoError(t, alice.DeleteAccount(ctx))

	var bundleLen int
	require.NoError(t, pool.QueryRow(ctx, `SELECT length(identity_bundle) FROM accounts WHERE id = $1`, alice.AccountID).Scan(&bundleLen))
	require.Zero(t, bundleLen)
}

func TestEndedSessionsAreGone(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, alice.Logout(ctx))
	live := sameIdentityNewInstance(t, baseURL, alice.Identity)
	require.NoError(t, live.Login(ctx))

	var rows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&rows))
	require.Equal(t, 1, rows, "logout deletes the session; only the live one is left")

	now := retention.NewPurger(pool, fakeClock{now: time.Now()}, retention.DefaultWindow)
	report, err := now.PurgeOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, report.Sessions, "a live session stays")
	_, err = live.GetAccount(ctx)
	require.NoError(t, err)

	idle := retention.NewPurger(pool, fakeClock{now: time.Now().Add(store.SessionIdleTimeout + time.Hour)}, retention.DefaultWindow)
	report, err = idle.PurgeOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, report.Sessions, "an expired session goes")
}

// ---- Invite approval is atomic and single-use ----

func inviteAdd(ws testclient.GroupView, owner, subjectClient *testclient.Client, proof []byte) testclient.GroupEntry {
	subject, subjectAgree := b64pub(subjectClient), subjectClient.AgreePub()
	return testclient.GroupEntry{
		V: 1, Group: ws.ID, Seq: ws.HeadSeq + 1, Prev: ws.HeadHash, Type: "add",
		Author: b64pub(owner), Subject: subject, SubjectAgree: subjectAgree, Epoch: ws.Epoch,
		Envelopes:   []testclient.GroupEnvelope{{Recipient: subject, HPKECiphertext: "Y3Q"}},
		InviteProof: b64raw(proof),
		Ts:          time.Now().UnixMilli(),
	}
}

func TestInviteApprovalIsAtomicAndSingleUse(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob, carol := newClient(t, baseURL), newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	require.NoError(t, carol.Signup(ctx, []byte("c")))
	ws := createList(t, ctx, alice)

	_, inviteID, mac := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, ws.ID, inviteID, []byte("pkg"), time.Now().Add(time.Hour))
	require.NoError(t, err)
	bobMAC, carolMAC := mac([]byte(inviteID+b64pub(bob))), mac([]byte(inviteID+b64pub(carol)))
	_, err = bob.JoinInvite(ctx, inviteID, []byte("n"), bobMAC)
	require.NoError(t, err)
	_, err = carol.JoinInvite(ctx, inviteID, []byte("n"), carolMAC)
	require.NoError(t, err)

	// A proof is bound to its requester: bob's proof can't admit carol.
	_, err = alice.AppendLog(ctx, ws.ID, inviteAdd(ws, alice, carol, bobMAC))
	requireAPIError(t, err, "forbidden")

	ws, err = alice.AppendLog(ctx, ws.ID, inviteAdd(ws, alice, bob, bobMAC))
	require.NoError(t, err)

	// Consumed: the other pending request is no longer listed or approvable,
	// and a rejected approval changes nothing.
	pending, err := alice.ListJoinRequests(ctx, ws.ID)
	require.NoError(t, err)
	require.Empty(t, pending)
	_, err = alice.AppendLog(ctx, ws.ID, inviteAdd(ws, alice, carol, carolMAC))
	requireAPIError(t, err, "forbidden")
	members, err := alice.ListGroupMembers(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, members, 2)
}

func TestRevokedInviteCannotBeApproved(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob := newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	ws := createList(t, ctx, alice)

	_, inviteID, mac := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, ws.ID, inviteID, []byte("pkg"), time.Now().Add(time.Hour))
	require.NoError(t, err)
	bobMAC := mac([]byte(inviteID + b64pub(bob)))
	_, err = bob.JoinInvite(ctx, inviteID, []byte("n"), bobMAC)
	require.NoError(t, err)
	require.NoError(t, alice.RevokeInvite(ctx, inviteID))

	pending, err := alice.ListJoinRequests(ctx, ws.ID)
	require.NoError(t, err)
	require.Empty(t, pending)
	_, err = alice.AppendLog(ctx, ws.ID, inviteAdd(ws, alice, bob, bobMAC))
	requireAPIError(t, err, "forbidden")
}

func TestDeletedRequesterDoesNotBreakListing(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob, carol := newClient(t, baseURL), newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	require.NoError(t, carol.Signup(ctx, []byte("c")))
	ws := createList(t, ctx, alice)

	_, inviteID, mac := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, ws.ID, inviteID, []byte("pkg"), time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = bob.JoinInvite(ctx, inviteID, []byte("n"), mac([]byte(inviteID+b64pub(bob))))
	require.NoError(t, err)
	_, err = carol.JoinInvite(ctx, inviteID, []byte("n"), mac([]byte(inviteID+b64pub(carol))))
	require.NoError(t, err)
	require.NoError(t, bob.DeleteAccount(ctx))

	pending, err := alice.ListJoinRequests(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, carol.AccountID, pending[0].AccountID)
}

// ---- Appends are tied to the caller ----

func TestAppendTiedToCaller(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob := newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	ws := createList(t, ctx, alice)
	ws = addMember(t, ctx, alice, ws, bob)

	// Unknown group: same 403 as a real one, so ids can't be probed.
	_, err := bob.AppendLog(ctx, strings.Repeat("0", 64), testclient.GroupEntry{V: 1, Type: "rotate", Author: b64pub(bob), Ts: time.Now().UnixMilli()})
	requireAPIError(t, err, "forbidden")

	// An entry signed by alice's key, submitted by bob's session.
	raw, sig, err := alice.SignGroupEntry(testclient.GroupEntry{
		V: 1, Group: ws.ID, Seq: ws.HeadSeq + 1, Prev: ws.HeadHash, Type: "rotate",
		Author: b64pub(alice), Epoch: ws.Epoch + 1,
		Envelopes: []testclient.GroupEnvelope{
			{Recipient: b64pub(alice), HPKECiphertext: "Y3Q"}, {Recipient: b64pub(bob), HPKECiphertext: "Y3Q"},
		},
		KeyChain: "a2V5Y2hhaW4", Ts: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	_, err = bob.AppendLogRaw(ctx, ws.ID, raw, sig)
	requireAPIError(t, err, "forbidden")
	_, err = alice.AppendLogRaw(ctx, ws.ID, raw, sig)
	require.NoError(t, err, "the same entry from its author is fine")
}

// ---- Rekey and login are serialized ----

// A login that verified against the old key while a rekey was committing
// must not end up with a live session.
func TestLoginDuringRekeyGetsNoSession(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	other := sameIdentityNewInstance(t, baseURL, alice.Identity)

	// Stands in for a rekey transaction, paused before commit.
	id, err := testclient.NewIdentity()
	require.NoError(t, err)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `UPDATE accounts SET auth_pub = $1 WHERE id = $2`, []byte(id.AuthPub), alice.AccountID)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- other.Login(ctx) }()
	waitForLockWait(t, ctx, pool)
	require.NoError(t, tx.Commit(ctx))

	requireAPIError(t, <-done, "invalid_credentials")
}

// ---- Login signatures are bound to the server they were made for ----

func TestLoginSignatureBoundToServerURL(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))

	// A signature made for another server (e.g. relayed by it) fails here.
	nonce, err := alice.Challenge(ctx)
	require.NoError(t, err)
	relayed := ed25519.Sign(alice.Identity.AuthPriv, auth.LoginMessage("https://other.example", nonce))
	requireAPIError(t, alice.RawLogin(ctx, nonce, relayed), "invalid_credentials")

	nonce, err = alice.Challenge(ctx)
	require.NoError(t, err)
	require.NoError(t, alice.RawLogin(ctx, nonce, alice.SignLogin(nonce)))
}

// ---- pre-launch hardening ----

func TestSecurityHeaders(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)

	req, err := http.NewRequest(http.MethodGet, baseURL+"/healthz", nil)
	require.NoError(t, err)
	req.Header.Set("X-Forwarded-Proto", "https") // as the TLS proxy sends it
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))
	require.Equal(t, "default-src 'none'; frame-ancestors 'none'", resp.Header.Get("Content-Security-Policy"))
	require.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"))
	require.Contains(t, resp.Header.Get("Strict-Transport-Security"), "max-age=31536000")
}

// Only configured proxies may set the client IP: a private-range peer that
// isn't in TRUSTED_PROXIES is keyed on its own address.
func TestOnlyConfiguredProxiesTrusted(t *testing.T) {
	_, pool := newTestServer(t, auth.DefaultChallengeTTL)
	lim := testLimits
	lim.ChallengesPerIPPerMinute = 20
	e := server.New(pool, server.Options{Origin: testOrigin, PublicURL: "http://unused", TrustedProxies: testProxies, Limits: lim})

	challenge := func(peer, xff string) int {
		pub, _, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/challenge",
			strings.NewReader(`{"auth_pub":"`+b64raw(pub)+`"}`))
		req.RemoteAddr = peer + ":40000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := range 20 {
		require.Equal(t, http.StatusOK, challenge("192.168.1.5", "198.51.100."+strconv.Itoa(i)))
	}
	require.Equal(t, http.StatusTooManyRequests, challenge("192.168.1.5", "198.51.100.99"))
}

// Request logs carry the route pattern, never the raw path: an invite id in
// the URL is the invite credential.
func TestLogsOmitURLCredentials(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	anon := newClient(t, baseURL)
	_, _ = anon.GetInvitePackage(ctx, "secret-invite-id-123")

	require.Contains(t, buf.String(), "/v1/invites/:invite_id")
	require.NotContains(t, buf.String(), "secret-invite-id-123")
}

// ---- Clients learn of membership changes without polling ----

func TestLiveGroupFrames(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob, carol := newClient(t, baseURL), newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	require.NoError(t, carol.Signup(ctx, []byte("c")))
	ws := createList(t, ctx, alice)
	ws = addMember(t, ctx, alice, ws, bob)

	alice2 := sameIdentityNewInstance(t, baseURL, alice.Identity)
	require.NoError(t, alice2.Login(ctx))
	alice2Conn, err := alice2.DialSync(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = alice2Conn.Close() })
	_, err = alice2Conn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "group" && f["group"] == ws.ID })
	require.NoError(t, err, "connect sends a group frame per list")
	created := createList(t, ctx, alice)
	_, err = alice2Conn.RecvUntil(ctx, func(f map[string]any) bool {
		return f["t"] == "group" && f["group"] == created.ID && f["head_seq"] == float64(created.HeadSeq)
	})
	require.NoError(t, err, "list creation must be pushed to the creator's other devices")

	bobConn := dialSynced(t, ctx, bob, ws.ID)
	ws = addMember(t, ctx, alice, ws, carol)
	isGroupAt := func(seq int64) func(map[string]any) bool {
		return func(f map[string]any) bool {
			return f["t"] == "group" && f["group"] == ws.ID && f["head_seq"] == float64(seq)
		}
	}
	_, err = bobConn.RecvUntil(ctx, isGroupAt(ws.HeadSeq))
	require.NoError(t, err, "log growth must be pushed to members")

	require.NoError(t, alice.DeleteGroup(ctx, ws.ID))
	_, err = bobConn.RecvUntil(ctx, isGroupAt(ws.HeadSeq))
	require.NoError(t, err, "deletion must be pushed to members")
}

// ---- Encryption keys are signed into the log, and must be the real ones ----

func TestAgreementKeysMustMatchAccounts(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob, mallory := newClient(t, baseURL), newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	require.NoError(t, mallory.Signup(ctx, []byte("m")))

	// A genesis naming someone else's encryption key as the creator's.
	_, err := alice.CreateGroup(ctx, testclient.GroupEntry{
		V: 1, Type: "create", Author: b64pub(alice), AuthorAgree: mallory.AgreePub(), Ts: time.Now().UnixMilli(),
	})
	requireAPIError(t, err, "bad_request")

	// An add wrapping bob's key to mallory's encryption key.
	ws := createList(t, ctx, alice)
	_, err = alice.AppendLog(ctx, ws.ID, testclient.GroupEntry{
		V: 1, Group: ws.ID, Seq: ws.HeadSeq + 1, Prev: ws.HeadHash, Type: "add",
		Author: b64pub(alice), Subject: b64pub(bob), SubjectAgree: mallory.AgreePub(), Epoch: ws.Epoch,
		Envelopes: []testclient.GroupEnvelope{{Recipient: b64pub(bob), HPKECiphertext: "Y3Q"}},
		Ts:        time.Now().UnixMilli(),
	})
	requireAPIError(t, err, "bad_request")

	// The stored log carries the real keys, for clients to use.
	addMember(t, ctx, alice, ws, bob)
	log, err := alice.GetLog(ctx, ws.ID)
	require.NoError(t, err)
	raw, err := base64.RawURLEncoding.DecodeString(log[1].Entry)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"subject_agree":"`+bob.AgreePub()+`"`)
}

// ---- Update headers are exact, typed and relayed unchanged ----

func sendRawHeader(t *testing.T, ctx context.Context, conn *testclient.SyncConn, frameType, cid string, header []byte) {
	t.Helper()
	require.NoError(t, conn.Send(ctx, map[string]any{
		"t": frameType, "cid": cid, "header": b64raw(header), "nonce": "bm9uY2U", "ct": "x", "sig": "c2ln",
	}))
}

func TestHeaderMustBeCanonicalAndTyped(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	ws := createList(t, ctx, alice)
	conn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	good := `{"type":"update","list":"` + ws.ID + `","epoch":0,"author":"` + b64pub(alice) + `","log_head":""}`
	for cid, header := range map[string]string{
		"case":      strings.Replace(good, `"epoch":0`, `"epoch":0,"Epoch":1`, 1),
		"duplicate": strings.Replace(good, `"log_head":""`, `"log_head":"","log_head":""`, 1),
		"spaces":    strings.Replace(good, `,"epoch"`, `, "epoch"`, 1),
		"type":      strings.Replace(good, `"type":"update"`, `"type":"snapshot"`, 1),
		"covers":    strings.Replace(good, `"epoch":0,`, `"epoch":0,"covers_seq":0,`, 1),
	} {
		sendRawHeader(t, ctx, conn, "update", cid, []byte(header))
		_, err := conn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "error" && f["cid"] == cid && f["code"] == "bad-request" })
		require.NoError(t, err, cid)
	}

	sendRawHeader(t, ctx, conn, "update", "ok", []byte(good))
	_, err = conn.RecvUntil(ctx, isAck("ok"))
	require.NoError(t, err)
}

// The header reaches other members as the exact bytes the author signed —
// including characters encoding/json would have escaped on re-encoding.
func TestHeaderRelayedByteForByte(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob := newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	ws := createList(t, ctx, alice)
	ws = addMember(t, ctx, alice, ws, bob)

	bobConn := dialSynced(t, ctx, bob, ws.ID)
	aliceConn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = aliceConn.Close() }()

	header := []byte(`{"type":"update","list":"` + ws.ID + `","epoch":0,"author":"` + b64pub(alice) + `","log_head":"<a&b>"}`)
	sendRawHeader(t, ctx, aliceConn, "update", "c1", header)

	f, err := bobConn.RecvUntil(ctx, isType("update"))
	require.NoError(t, err)
	relayed, err := base64.RawURLEncoding.DecodeString(f["msg"].(map[string]any)["header"].(string))
	require.NoError(t, err)
	require.Equal(t, string(header), string(relayed))
}
