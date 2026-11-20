package e2e_test

// Deletion is immediate: a deleted list or account leaves no row behind,
// while lists that survive keep the history the account wrote in them.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/testclient"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// countRows returns, per query, how many rows it counts with arg.
func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arg any, queries ...string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, q := range queries {
		var n int
		require.NoError(t, pool.QueryRow(ctx, q, arg).Scan(&n))
		out[q] = n
	}
	return out
}

func zeroes(queries []string) map[string]int {
	out := map[string]int{}
	for _, q := range queries {
		out[q] = 0
	}
	return out
}

// Every row a list owns.
var listRows = []string{
	`SELECT count(*) FROM groups WHERE id = $1`,
	`SELECT count(*) FROM group_log WHERE group_id = $1`,
	`SELECT count(*) FROM memberships WHERE group_id = $1`,
	`SELECT count(*) FROM list_updates WHERE list_id = $1`,
	`SELECT count(*) FROM list_snapshots WHERE list_id = $1`,
	`SELECT count(*) FROM invites WHERE group_id = $1`,
	`SELECT count(*) FROM group_invites WHERE group_id = $1`,
	`SELECT count(*) FROM join_requests WHERE invite_id IN (SELECT id FROM invites WHERE group_id = $1)`,
}

// Every row about an account.
var accountRows = []string{
	`SELECT count(*) FROM accounts WHERE id = $1`,
	`SELECT count(*) FROM sessions WHERE account_id = $1`,
	`SELECT count(*) FROM join_requests WHERE account_id = $1`,
	`SELECT count(*) FROM memberships WHERE account_id = $1`,
	`SELECT count(*) FROM challenges WHERE subject = $1::text`,
}

func sendUpdate(t *testing.T, ctx context.Context, c *testclient.Client, l testclient.GroupView, cid string) {
	t.Helper()
	conn, err := c.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SendUpdate(ctx, cid, c.UpdateHeader(l.ID, l.Epoch, ""), cid))
	_, err = conn.RecvUntil(ctx, isAck(cid))
	require.NoError(t, err)
}

// Bob joined Alice's list through an invite (an approved join request),
// wrote in it, started a rekey and then deletes his account: refused until
// he leaves, then nothing about him remains but what he signed into the
// list that survives, which Alice can still read.
func TestDeletedMemberLeavesOnlySignedHistory(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob := newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	l := createList(t, ctx, alice)

	_, inviteID, mac := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, l.ID, inviteID, []byte("pkg"), time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = bob.JoinInvite(ctx, inviteID, []byte("n"), mac([]byte(inviteID+b64pub(bob))))
	require.NoError(t, err)
	pending, err := alice.ListJoinRequests(ctx, l.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	l = approveJoinRequest(t, ctx, alice, l, pending[0])
	sendUpdate(t, ctx, bob, l, "by-bob")
	// A second device of Bob's, and a rekey challenge left pending.
	other := sameIdentityNewInstance(t, baseURL, bob.Identity)
	require.NoError(t, other.Login(ctx))
	require.NoError(t, bob.Do(ctx, "POST", "/v1/account/rekey/challenge", nil, nil))

	requireAPIError(t, bob.DeleteAccount(ctx), "account_has_lists")
	for q, n := range countRows(t, ctx, pool, bob.AccountID, accountRows...) {
		require.Positive(t, n, "a refused deletion changes nothing: %s", q)
	}

	l, err = bob.AppendLog(ctx, l.ID, testclient.GroupEntry{
		V: 1, Group: l.ID, Seq: l.HeadSeq + 1, Prev: l.HeadHash, Type: "leave",
		Author: b64pub(bob), Subject: b64pub(bob), Epoch: l.Epoch, Ts: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	require.NoError(t, bob.DeleteAccount(ctx))

	require.Equal(t, zeroes(accountRows), countRows(t, ctx, pool, bob.AccountID, accountRows...))
	_, err = other.GetAccount(ctx)
	require.Error(t, err, "the other device is signed out")
	require.Error(t, sameIdentityNewInstance(t, baseURL, bob.Identity).Login(ctx), "the key no longer logs in")

	// Alice's list keeps Bob's update and the log entries naming him.
	require.Equal(t, 1, countRows(t, ctx, pool, l.ID, listRows[3])[listRows[3]])
	log, err := alice.GetLog(ctx, l.ID)
	require.NoError(t, err)
	require.Len(t, log, 3, "create, the add approving Bob, his leave")
	for _, e := range log[1:] {
		entry, err := base64.RawURLEncoding.DecodeString(e.Entry)
		require.NoError(t, err)
		sig, err := base64.RawURLEncoding.DecodeString(e.Sig)
		require.NoError(t, err)
		signer := alice.Identity.SignPub
		if e.Seq == 2 {
			signer = bob.Identity.SignPub
		}
		require.True(t, ed25519.Verify(signer, entry, sig), "entry %d still verifies", e.Seq)
	}
	members, err := alice.ListGroupMembers(ctx, l.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)

	// And his update, with his key in its signed header, is still served.
	conn, err := alice.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.Sub(ctx, l.ID, 0))
	f, err := conn.RecvUntil(ctx, isType("update"))
	require.NoError(t, err)
	raw, err := base64.RawURLEncoding.DecodeString(f["msg"].(map[string]any)["header"].(string))
	require.NoError(t, err)
	var header testclient.UpdateHeader
	require.NoError(t, json.Unmarshal(raw, &header))
	require.Equal(t, b64pub(bob), header.Author)
}

// Alice owns a list shared with Bob, with an update, a snapshot, a link
// invite and its package, and a pending join request from Carol. Deleting
// her account deletes it all at once and tells Bob.
func TestDeletedOwnerTakesTheirListsAlong(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob, carol := newClient(t, baseURL), newClient(t, baseURL), newClient(t, baseURL)
	for _, c := range []*testclient.Client{alice, bob, carol} {
		require.NoError(t, c.Signup(ctx, []byte("x")))
	}
	l := fullList(t, ctx, alice, bob, carol)
	bobConn := dialSynced(t, ctx, bob, l.ID)

	require.NoError(t, alice.DeleteAccount(ctx))

	_, err := bobConn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "group" && f["group"] == l.ID })
	require.NoError(t, err, "members are told the list changed")
	require.Equal(t, zeroes(listRows), countRows(t, ctx, pool, l.ID, listRows...))
	require.Equal(t, zeroes(listRows), countRows(t, ctx, pool, alice.InboxID, listRows...))
	require.Equal(t, zeroes(accountRows), countRows(t, ctx, pool, alice.AccountID, accountRows...))
	require.Equal(t, zeroes(accountRows[2:3]), countRows(t, ctx, pool, carol.AccountID, accountRows[2]), "Carol's request went with the list")
	_, err = bob.GetAccount(ctx)
	require.NoError(t, err, "the members' accounts are untouched")
}

// The owner deleting one list removes it and everything in it at once.
func TestDeletedListLeavesNoRow(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob, carol := newClient(t, baseURL), newClient(t, baseURL), newClient(t, baseURL)
	for _, c := range []*testclient.Client{alice, bob, carol} {
		require.NoError(t, c.Signup(ctx, []byte("x")))
	}
	l := fullList(t, ctx, alice, bob, carol)
	kept := createList(t, ctx, alice)
	bobConn := dialSynced(t, ctx, bob, l.ID)

	requireAPIError(t, bob.DeleteGroup(ctx, l.ID), "forbidden")
	require.NoError(t, alice.DeleteGroup(ctx, l.ID))

	_, err := bobConn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "group" && f["group"] == l.ID })
	require.NoError(t, err)
	require.Equal(t, zeroes(listRows), countRows(t, ctx, pool, l.ID, listRows...))
	require.Equal(t, 1, countRows(t, ctx, pool, kept.ID, listRows[0])[listRows[0]], "other lists stay")
	requireAPIError(t, alice.DeleteGroup(ctx, l.ID), "forbidden")
}

// fullList is a list of owner's shared with member, holding an update by
// each, a snapshot, an open link invite with its package and a pending
// join request from requester on an approve-first invite.
func fullList(t *testing.T, ctx context.Context, owner, member, requester *testclient.Client) testclient.GroupView {
	t.Helper()
	l := createList(t, ctx, owner)
	l = addMember(t, ctx, owner, l, member)
	sendUpdate(t, ctx, owner, l, "by-owner")
	sendUpdate(t, ctx, member, l, "by-member")

	conn, err := owner.DialSync(ctx)
	require.NoError(t, err)
	require.NoError(t, conn.SendSnapshot(ctx, "s1", owner.UpdateHeader(l.ID, l.Epoch, ""), 2, "snap"))
	_, err = conn.RecvUntil(ctx, isAck("s1"))
	require.NoError(t, err)
	_ = conn.Close()

	_, l = openLinkInvite(t, ctx, owner, l)

	_, inviteID, mac := testInviteSecret(t)
	_, err = owner.CreateInvite(ctx, l.ID, inviteID, []byte("pkg"), time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = requester.JoinInvite(ctx, inviteID, []byte("n"), mac([]byte(inviteID+b64pub(requester))))
	require.NoError(t, err)

	got := countRows(t, ctx, shared.pool, l.ID, listRows...)
	for q, n := range got {
		require.Positive(t, n, q)
	}
	return l
}
