package e2e_test

import (
	"context"
	"testing"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/retention"
	"runer-api/internal/testclient"

	"github.com/stretchr/testify/require"
)

// removeFromGroup appends a `remove` for subject, with envelopes for every
// OTHER currently remaining member (rule 4: envelopes for every remaining
// member, i.e. every current member except subject).
func removeFromGroup(t *testing.T, ctx context.Context, owner *testclient.Client, g testclient.GroupView, subject *testclient.Client, otherRemaining ...*testclient.Client) testclient.GroupView {
	t.Helper()
	envelopes := []testclient.GroupEnvelope{{Recipient: b64pub(owner), HPKECiphertext: "Y3Q"}}
	for _, m := range otherRemaining {
		envelopes = append(envelopes, testclient.GroupEnvelope{Recipient: b64pub(m), HPKECiphertext: "Y3Q"})
	}
	entry := testclient.GroupEntry{
		V: 1, Group: g.ID, Seq: g.HeadSeq + 1, Prev: g.HeadHash, Type: "remove",
		Author: b64pub(owner), Subject: b64pub(subject), Epoch: g.Epoch + 1,
		Envelopes: envelopes,
		KeyChain:  "a2V5Y2hhaW4",
		Ts:        time.Now().UnixMilli(),
	}
	updated, err := owner.AppendLog(ctx, g.ID, entry)
	require.NoError(t, err)
	return updated
}

func TestRemovedMemberSocketClosed(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	ws = addMember(t, ctx, alice, ws, bob)

	bobConn, err := bob.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = bobConn.Close() }()
	require.NoError(t, bobConn.Sub(ctx, ws.ID, 0))
	_, err = bobConn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)

	removeFromGroup(t, ctx, alice, ws, bob)

	// E2EE §8.1: "close Bob's sockets" — the server proactively closes
	// his live connection, not just denies future writes.
	bye, err := bobConn.RecvUntil(ctx, isType("bye"))
	require.NoError(t, err)
	require.Equal(t, "remove", bye["reason"])

	_, err = bobConn.Recv(ctx)
	require.Error(t, err, "connection must actually be closed after bye")
}

func TestLeaveThenNextMemberRotates(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	ws = addMember(t, ctx, alice, ws, bob)

	// Bob leaves. He must not create the next key himself (rule: leave
	// doesn't change epoch), so the epoch here stays put.
	leave := testclient.GroupEntry{
		V: 1, Group: ws.ID, Seq: ws.HeadSeq + 1, Prev: ws.HeadHash, Type: "leave",
		Author: b64pub(bob), Subject: b64pub(bob), Epoch: ws.Epoch,
		Ts: time.Now().UnixMilli(),
	}
	ws, err := bob.AppendLog(ctx, ws.ID, leave)
	require.NoError(t, err)
	require.Equal(t, int64(0), ws.Epoch)

	members, err := alice.ListGroupMembers(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)

	// Any remaining member may rotate.
	rotate := testclient.GroupEntry{
		V: 1, Group: ws.ID, Seq: ws.HeadSeq + 1, Prev: ws.HeadHash, Type: "rotate",
		Author: b64pub(alice), Epoch: ws.Epoch + 1,
		Envelopes: []testclient.GroupEnvelope{{Recipient: b64pub(alice), HPKECiphertext: "Y3Q"}},
		KeyChain:  "a2V5Y2hhaW4",
		Ts:        time.Now().UnixMilli(),
	}
	ws, err = alice.AppendLog(ctx, ws.ID, rotate)
	require.NoError(t, err)
	require.Equal(t, int64(1), ws.Epoch)

	// Bob, who left, can no longer write under the new epoch.
	staleUpdate := bob.UpdateHeader(ws.ID, 0, "")
	conn, err := bob.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SendUpdate(ctx, "x", staleUpdate, "content"))
	errFrame, err := conn.RecvUntil(ctx, isType("error"))
	require.NoError(t, err)
	require.Equal(t, "forbidden", errFrame["code"])
}

// The list app's acceptance scenario: the owner shares a
// list with two people and removes one. The removed person can neither
// read nor write anything new; the one who stays keeps working under the
// new epoch.
func TestShareWithTwoThenRemoveOne(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	carol := newClient(t, baseURL)
	require.NoError(t, carol.Signup(ctx, []byte("carol-bundle")))

	l := createList(t, ctx, alice)
	l = addMember(t, ctx, alice, l, bob)
	l = addMember(t, ctx, alice, l, carol)

	carolConn, err := carol.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = carolConn.Close() }()
	require.NoError(t, carolConn.Sub(ctx, l.ID, 0))
	_, err = carolConn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)

	l = removeFromGroup(t, ctx, alice, l, bob, carol)
	require.Equal(t, int64(1), l.Epoch)

	// Carol learns the log grew and keeps writing under the new key.
	_, err = carolConn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "group" && f["group"] == l.ID })
	require.NoError(t, err)
	require.NoError(t, carolConn.SendUpdate(ctx, "c1", carol.UpdateHeader(l.ID, l.Epoch, ""), "after-removal"))
	_, err = carolConn.RecvUntil(ctx, isAck("c1"))
	require.NoError(t, err)

	// Bob sees nothing new: not listed, can't subscribe, can't write.
	lists := sharedLists(t, ctx, bob)
	require.Empty(t, lists)
	_, err = bob.GetLog(ctx, l.ID)
	requireAPIError(t, err, "forbidden")

	bobConn, err := bob.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = bobConn.Close() }()
	require.NoError(t, bobConn.Sub(ctx, l.ID, 0))
	errFrame, err := bobConn.RecvUntil(ctx, isType("error"))
	require.NoError(t, err)
	require.Equal(t, "forbidden", errFrame["code"])
	require.NoError(t, bobConn.SendUpdate(ctx, "b1", bob.UpdateHeader(l.ID, l.Epoch, ""), "sneaky"))
	errFrame, err = bobConn.RecvUntil(ctx, isErrorCode("forbidden"))
	require.NoError(t, err)
	require.Equal(t, "b1", errFrame["cid"])
}

// Rule 6 over HTTP: the owner cannot leave; they delete the list instead.
func TestOwnerCannotLeave(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	l := createList(t, ctx, alice)

	leave := testclient.GroupEntry{
		V: 1, Group: l.ID, Seq: l.HeadSeq + 1, Prev: l.HeadHash, Type: "leave",
		Author: b64pub(alice), Subject: b64pub(alice), Epoch: l.Epoch, Ts: time.Now().UnixMilli(),
	}
	_, err := alice.AppendLog(ctx, l.ID, leave)
	requireAPIError(t, err, "forbidden")
}

func TestDeleteAccountRevokesSessionAndBlocksLogin(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))

	require.NoError(t, alice.DeleteAccount(ctx))

	_, err := alice.ListSessions(ctx)
	require.Error(t, err, "session must be revoked immediately")

	fresh := sameIdentityNewInstance(t, baseURL, alice.Identity)
	err = fresh.Login(ctx)
	require.Error(t, err, "a deleted account must no longer be able to log in")
}

// Deleting an account deletes the lists it owns at once, for every member,
// and revokes their invites.
func TestDeleteAccountDeletesOwnedLists(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice, bob := newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	l := createList(t, ctx, alice)
	l = addMember(t, ctx, alice, l, bob)
	_, inviteID, _ := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, l.ID, inviteID, []byte("pkg"), time.Now().Add(time.Hour))
	require.NoError(t, err)
	bobConn := dialSynced(t, ctx, bob, l.ID)

	require.NoError(t, alice.DeleteAccount(ctx))

	_, err = bobConn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "group" && f["group"] == l.ID })
	require.NoError(t, err, "members are told the list changed")
	require.Empty(t, sharedLists(t, ctx, bob))
	_, err = bob.ListGroupMembers(ctx, l.ID)
	requireAPIError(t, err, "forbidden")
	_, err = bob.GetInvitePackage(ctx, inviteID)
	require.Error(t, err, "the deleted list's invite must be gone")

	// With the list gone bob is in none, so he can delete his account too.
	require.NoError(t, bob.DeleteAccount(ctx))
}

// A member of a list it doesn't own can't be deleted until it leaves: only
// a signed leave takes a member out of a log, and the server can't sign one.
func TestDeleteAccountRefusedWhileMember(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice, bob := newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	l := createList(t, ctx, alice)
	l = addMember(t, ctx, alice, l, bob)
	createList(t, ctx, bob)

	requireAPIError(t, bob.DeleteAccount(ctx), "account_has_lists")
	_, err := bob.ListSessions(ctx)
	require.NoError(t, err, "a refused deletion must leave the session alone")
	require.Len(t, sharedLists(t, ctx, bob), 2, "a refused deletion must leave the account's own lists alone")

	_, err = bob.AppendLog(ctx, l.ID, testclient.GroupEntry{
		V: 1, Group: l.ID, Seq: l.HeadSeq + 1, Prev: l.HeadHash, Type: "leave",
		Author: b64pub(bob), Subject: b64pub(bob), Epoch: l.Epoch, Ts: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	require.NoError(t, bob.DeleteAccount(ctx))

	_, err = alice.ListGroupMembers(ctx, l.ID)
	require.NoError(t, err, "the list bob left is untouched")
}

func TestDeleteListOwnerOnly(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	ws = addMember(t, ctx, alice, ws, bob)

	require.Error(t, bob.DeleteGroup(ctx, ws.ID), "a member cannot delete the list")

	require.NoError(t, alice.DeleteGroup(ctx, ws.ID))

	groups := sharedLists(t, ctx, alice)
	require.Empty(t, groups)
}

// fakeClock is a fixed-instant retention.Clock, so the purge window can be
// exercised deterministically instead of sleeping 30 days.
type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

func TestRetentionPurgesDeletedListAfterWindow(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	l := createList(t, ctx, alice)

	conn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	header := alice.UpdateHeader(l.ID, l.Epoch, "")
	require.NoError(t, conn.SendUpdate(ctx, "c1", header, "content"))
	_, err = conn.RecvUntil(ctx, isAck("c1"))
	require.NoError(t, err)

	require.NoError(t, alice.DeleteGroup(ctx, l.ID))

	count := func(query string) int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, query, l.ID).Scan(&n))
		return n
	}

	// Immediately after: not purged yet (window hasn't elapsed by the
	// purger's clock).
	report, err := retention.NewPurger(pool, fakeClock{now: time.Now()}, retention.DefaultWindow).PurgeOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, report.Groups)
	require.Equal(t, 1, count(`SELECT count(*) FROM groups WHERE id = $1`))

	// Fast-forward the purger's clock past the retention window.
	future := fakeClock{now: time.Now().Add(retention.DefaultWindow + time.Hour)}
	report, err = retention.NewPurger(pool, future, retention.DefaultWindow).PurgeOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, report.Groups)
	require.Equal(t, 0, count(`SELECT count(*) FROM groups WHERE id = $1`))
	require.Equal(t, 0, count(`SELECT count(*) FROM list_updates WHERE list_id = $1`))
}
