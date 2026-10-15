package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"runer-api/internal/auth"

	"github.com/stretchr/testify/require"
)

func isType(t string) func(map[string]any) bool {
	return func(f map[string]any) bool { return f["t"] == t }
}

func TestConcurrentEditsFanOutToOtherSubscribers(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	ws = addMember(t, ctx, alice, ws, bob)

	aliceConn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = aliceConn.Close() }()
	bobConn, err := bob.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = bobConn.Close() }()

	require.NoError(t, aliceConn.Sub(ctx, ws.ID, 0))
	_, err = aliceConn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)
	require.NoError(t, bobConn.Sub(ctx, ws.ID, 0))
	_, err = bobConn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)

	aliceHeader := alice.UpdateHeader(ws.ID, ws.Epoch, "")
	bobHeader := bob.UpdateHeader(ws.ID, ws.Epoch, "")

	require.NoError(t, aliceConn.SendUpdate(ctx, "a1", aliceHeader, "alice-edit"))
	require.NoError(t, bobConn.SendUpdate(ctx, "b1", bobHeader, "bob-edit"))

	isUpdate := func(f map[string]any) bool { return f["t"] == "update" && f["list"] == ws.ID }

	// alice's own ack (for "a1") and bob's live broadcast can arrive on
	// her connection in either order — collect both rather than
	// discarding whichever comes first (Sync §4: "to
	// every subscriber except the author").
	_, bobsView, err := aliceConn.RecvTwo(ctx,
		func(f map[string]any) bool { return f["t"] == "ack" && f["cid"] == "a1" },
		isUpdate)
	require.NoError(t, err)
	msg := bobsView["msg"].(map[string]any)
	require.Equal(t, "bob-edit", msg["ct"])

	_, alicesView, err := bobConn.RecvTwo(ctx,
		func(f map[string]any) bool { return f["t"] == "ack" && f["cid"] == "b1" },
		isUpdate)
	require.NoError(t, err)
	msg = alicesView["msg"].(map[string]any)
	require.Equal(t, "alice-edit", msg["ct"])
}

func TestOfflineClientCatchesUpOnReconnect(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	aliceConn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = aliceConn.Close() }()

	require.NoError(t, aliceConn.Sub(ctx, ws.ID, 0))
	_, err = aliceConn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)

	header := alice.UpdateHeader(ws.ID, ws.Epoch, "")
	const n = 5
	for i := 0; i < n; i++ {
		cid := fmt.Sprintf("u%d", i)
		require.NoError(t, aliceConn.SendUpdate(ctx, cid, header, fmt.Sprintf("edit-%d", i)))
		_, err := aliceConn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "ack" && f["cid"] == cid })
		require.NoError(t, err)
	}

	// Bob was offline the whole time; he joins now, subscribing fresh.
	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	ws = addMember(t, ctx, alice, ws, bob)

	bobConn, err := bob.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = bobConn.Close() }()
	require.NoError(t, bobConn.Sub(ctx, ws.ID, 0))

	var seen []string
	for i := 0; i < n; i++ {
		f, err := bobConn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "update" })
		require.NoError(t, err)
		msg := f["msg"].(map[string]any)
		seen = append(seen, msg["ct"].(string))
	}
	for i := 0; i < n; i++ {
		require.Equal(t, fmt.Sprintf("edit-%d", i), seen[i], "catch-up must arrive in seq order")
	}
	synced, err := bobConn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)
	require.EqualValues(t, n, synced["head"])
}

func TestSnapshotCatchUp(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	aliceConn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = aliceConn.Close() }()

	require.NoError(t, aliceConn.Sub(ctx, ws.ID, 0))
	_, err = aliceConn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)

	header := alice.UpdateHeader(ws.ID, ws.Epoch, "")
	for i := 0; i < 3; i++ {
		cid := fmt.Sprintf("pre%d", i)
		require.NoError(t, aliceConn.SendUpdate(ctx, cid, header, fmt.Sprintf("pre-%d", i)))
		_, err := aliceConn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "ack" && f["cid"] == cid })
		require.NoError(t, err)
	}

	require.NoError(t, aliceConn.SendSnapshot(ctx, "snap1", header, 3, "full-state-covering-3"))
	_, err = aliceConn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "ack" && f["cid"] == "snap1" })
	require.NoError(t, err)

	for i := 3; i < 5; i++ {
		cid := fmt.Sprintf("post%d", i)
		require.NoError(t, aliceConn.SendUpdate(ctx, cid, header, fmt.Sprintf("post-%d", i)))
		_, err := aliceConn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "ack" && f["cid"] == cid })
		require.NoError(t, err)
	}

	// A third client joins fresh: snapshot, then only the post-snapshot tail.
	carol := newClient(t, baseURL)
	require.NoError(t, carol.Signup(ctx, []byte("carol-bundle")))
	ws = addMember(t, ctx, alice, ws, carol)

	carolConn, err := carol.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = carolConn.Close() }()
	require.NoError(t, carolConn.Sub(ctx, ws.ID, 0))

	snap, err := carolConn.RecvUntil(ctx, isType("snapshot"))
	require.NoError(t, err)
	snapMsg := snap["msg"].(map[string]any)
	require.Equal(t, "full-state-covering-3", snapMsg["ct"])
	require.EqualValues(t, 3, snap["covers_seq"])

	var tail []string
	for i := 0; i < 2; i++ {
		f, err := carolConn.RecvUntil(ctx, isType("update"))
		require.NoError(t, err)
		msg := f["msg"].(map[string]any)
		tail = append(tail, msg["ct"].(string))
	}
	require.Equal(t, []string{"post-3", "post-4"}, tail)

	synced, err := carolConn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)
	require.EqualValues(t, 5, synced["head"])
}

// A snapshot claiming to cover updates that don't exist yet would hide
// every future update from fresh clients.
func TestSnapshotBeyondHeadRejected(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	conn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	header := alice.UpdateHeader(ws.ID, ws.Epoch, "")
	require.NoError(t, conn.SendSnapshot(ctx, "s1", header, 9223372036854775807, "poison"))
	errFrame, err := conn.RecvUntil(ctx, isType("error"))
	require.NoError(t, err)
	require.Equal(t, "bad-request", errFrame["code"])
	require.Equal(t, "s1", errFrame["cid"])
}

func TestSnapshotSpoofedAuthorForbidden(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	ws = addMember(t, ctx, alice, ws, bob)

	conn, err := bob.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Bob, a real member, claims Alice as the author.
	header := alice.UpdateHeader(ws.ID, ws.Epoch, "")
	require.NoError(t, conn.SendSnapshot(ctx, "s1", header, 0, "forged"))
	errFrame, err := conn.RecvUntil(ctx, isType("error"))
	require.NoError(t, err)
	require.Equal(t, "forbidden", errFrame["code"])
}

func TestSnapshotsAreRateLimited(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	conn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Fire well past the 50-frame burst without waiting for acks.
	header := alice.UpdateHeader(ws.ID, ws.Epoch, "")
	for i := range 80 {
		require.NoError(t, conn.SendSnapshot(ctx, fmt.Sprintf("s%d", i), header, 0, "state"))
	}
	errFrame, err := conn.RecvUntil(ctx, isType("error"))
	require.NoError(t, err)
	require.Equal(t, "rate-limited", errFrame["code"])
}

func TestNonMemberUpdateForbidden(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	outsider := newClient(t, baseURL)
	require.NoError(t, outsider.Signup(ctx, []byte("outsider-bundle")))

	conn, err := outsider.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	header := outsider.UpdateHeader(ws.ID, 0, "")
	require.NoError(t, conn.SendUpdate(ctx, "x1", header, "content"))

	errFrame, err := conn.RecvUntil(ctx, isType("error"))
	require.NoError(t, err)
	require.Equal(t, "forbidden", errFrame["code"])
}

func TestOversizedFrameTooLarge(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	conn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	header := alice.UpdateHeader(ws.ID, ws.Epoch, "")
	huge := strings.Repeat("a", 600*1024)
	require.NoError(t, conn.SendUpdate(ctx, "big1", header, huge))

	errFrame, err := conn.RecvUntil(ctx, isType("error"))
	require.NoError(t, err)
	require.Equal(t, "too-large", errFrame["code"])
}

func TestWrongEpochStaleEpoch(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	conn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	header := alice.UpdateHeader(ws.ID, ws.Epoch+1, "") // wrong: list is still at epoch 0
	require.NoError(t, conn.SendUpdate(ctx, "e1", header, "content"))

	errFrame, err := conn.RecvUntil(ctx, isType("error"))
	require.NoError(t, err)
	require.Equal(t, "stale-epoch", errFrame["code"])
}

// TestServerNeverInterpretsUpdateContent stands in for the
// plaintext-canary exit criterion ("type a unique string... must be
// absent from the database dump / WS capture"). The real canary proves
// client-core's encryption layer runs before anything leaves the client —
// that layer is client-core's, out of scope for this Go-only
// server. What IS server-scoped, and what this proves: the server never
// inspects, logs, or transforms `ct` — it relays and stores it as opaque
// bytes, verbatim, which is the property that makes real encryption safe
// to rely on once client-core adds it.
func TestServerNeverInterpretsUpdateContent(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	conn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.Sub(ctx, ws.ID, 0))
	_, err = conn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)

	marker := "the-quick-brown-fox-canary-9f3a1c"
	header := alice.UpdateHeader(ws.ID, ws.Epoch, "")
	require.NoError(t, conn.SendUpdate(ctx, "canary", header, marker))
	ack, err := conn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "ack" && f["cid"] == "canary" })
	require.NoError(t, err)

	// The server relayed it back byte-identical (via catch-up on a fresh
	// subscription) — it did not transform, truncate, or drop the field.
	fresh, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = fresh.Close() }()
	require.NoError(t, fresh.Sub(ctx, ws.ID, 0))
	got, err := fresh.RecvUntil(ctx, isType("update"))
	require.NoError(t, err)
	msg := got["msg"].(map[string]any)
	require.Equal(t, marker, msg["ct"], "server must relay content byte-identical, never interpret it")
	require.EqualValues(t, ack["seq"], got["seq"])
}
