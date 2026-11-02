package e2e_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/testclient"

	"github.com/stretchr/testify/require"
)

// linkInvite is the owner's side of a link invite (E2EE §6): the one-time
// key pair a real client derives from s, and the package row's id.
type linkInvite struct {
	pub      ed25519.PublicKey
	priv     ed25519.PrivateKey
	inviteID string
}

// openLinkInvite appends the owner's `invite` entry and stores the
// package, as the owner's app does before sharing the link.
func openLinkInvite(t *testing.T, ctx context.Context, owner *testclient.Client, l testclient.GroupView) (linkInvite, testclient.GroupView) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, inviteID, _ := testInviteSecret(t)
	expires := time.Now().Add(48 * time.Hour).Truncate(time.Millisecond)

	l, err = owner.AppendLog(ctx, l.ID, testclient.GroupEntry{
		V: 1, Group: l.ID, Seq: l.HeadSeq + 1, Prev: l.HeadHash, Type: "invite",
		Author: b64pub(owner), Epoch: l.Epoch, Invite: b64raw(pub), Expires: expires.UnixMilli(),
		Ts: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	_, err = owner.CreateLinkInvite(ctx, l.ID, inviteID, []byte("encrypted{list, log head, owner, name, epoch, list key}"), expires, pub)
	require.NoError(t, err)
	return linkInvite{pub: pub, priv: priv, inviteID: inviteID}, l
}

// joinEntry is the invitee's own `join`, built on the head the invite
// log shows. The proof is signed independently of the server's code:
// "runer/v1/join" ‖ 0x00 ‖ list id ‖ sign pub ‖ agree pub.
func joinEntry(t *testing.T, ctx context.Context, joiner *testclient.Client, inv linkInvite, signWith ed25519.PrivateKey) testclient.GroupEntry {
	t.Helper()
	log, err := joiner.InviteLog(ctx, inv.inviteID)
	require.NoError(t, err)
	require.NotEmpty(t, log)
	head := log[len(log)-1]
	listID := groupIDOf(t, log[0])

	rawList, err := hex.DecodeString(listID)
	require.NoError(t, err)
	msg := append([]byte("runer/v1/join\x00"), rawList...)
	msg = append(msg, joiner.Identity.SignPub...)
	msg = append(msg, joiner.Identity.AgreePub...)

	return testclient.GroupEntry{
		V: 1, Group: listID, Seq: head.Seq + 1, Prev: head.Hash, Type: "join",
		Author: b64pub(joiner), Subject: b64pub(joiner), SubjectAgree: joiner.AgreePub(), Epoch: epochAt(t, log),
		Invite: b64raw(inv.pub), InviteProof: b64raw(ed25519.Sign(signWith, msg)),
		Ts: time.Now().UnixMilli(),
	}
}

// The case the feature exists for: Bob joins while Alice's app is closed,
// then reads and writes the list without Alice ever coming back online.
func TestLinkInviteJoinWhileOwnerOffline(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	l := createList(t, ctx, alice)
	inv, l := openLinkInvite(t, ctx, alice, l)
	require.NoError(t, alice.Logout(ctx)) // Alice is offline from here on

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	_, err := bob.GetInvitePackage(ctx, inv.inviteID)
	require.NoError(t, err)

	joined, err := bob.AcceptInvite(ctx, inv.inviteID, joinEntry(t, ctx, bob, inv, inv.priv))
	require.NoError(t, err)
	require.Equal(t, l.HeadSeq+1, joined.HeadSeq)
	require.Equal(t, l.Epoch, joined.Epoch)

	// Bob is a member: he can subscribe and write under the key the
	// package carried, and the list shows up for him.
	lists := sharedLists(t, ctx, bob)
	require.Len(t, lists, 1)
	conn, err := bob.DialSync(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.Sub(ctx, l.ID, 0))
	_, err = conn.RecvUntil(ctx, isType("synced"))
	require.NoError(t, err)
	require.NoError(t, conn.SendUpdate(ctx, "b1", bob.UpdateHeader(l.ID, joined.Epoch, joined.HeadHash), "bob-edit"))
	ack, err := conn.RecvUntil(ctx, isType("ack"))
	require.NoError(t, err)
	require.Equal(t, "b1", ack["cid"])

	// Single use: the package (it holds the list key) is no longer served,
	// and nobody else can join with the same invite.
	_, err = bob.GetInvitePackage(ctx, inv.inviteID)
	requireAPIError(t, err, "invite_not_usable")
	carol := newClient(t, baseURL)
	require.NoError(t, carol.Signup(ctx, []byte("carol-bundle")))
	_, err = carol.InviteLog(ctx, inv.inviteID)
	requireAPIError(t, err, "invite_not_usable")

	// When Alice comes back, the log tells her Bob joined.
	require.NoError(t, alice.Login(ctx))
	members, err := alice.ListGroupMembers(ctx, l.ID)
	require.NoError(t, err)
	require.Len(t, members, 2)
}

func TestLinkInviteClosedByUninviteOrNewEpoch(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	l := createList(t, ctx, alice)

	// Uninvite: the log closes the invite and the package stops being served.
	inv, l := openLinkInvite(t, ctx, alice, l)
	entry := joinEntry(t, ctx, bob, inv, inv.priv) // Bob opened the link before it was revoked
	l, err := alice.AppendLog(ctx, l.ID, testclient.GroupEntry{
		V: 1, Group: l.ID, Seq: l.HeadSeq + 1, Prev: l.HeadHash, Type: "uninvite",
		Author: b64pub(alice), Epoch: l.Epoch, Invite: b64raw(inv.pub), Ts: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	_, err = bob.AcceptInvite(ctx, inv.inviteID, entry)
	requireAPIError(t, err, "invite_not_usable")
	_, err = bob.GetInvitePackage(ctx, inv.inviteID)
	requireAPIError(t, err, "invite_not_usable")

	// A new epoch voids open invites: their packages carry the old key.
	inv2, l := openLinkInvite(t, ctx, alice, l)
	entry = joinEntry(t, ctx, bob, inv2, inv2.priv)
	_, err = alice.AppendLog(ctx, l.ID, testclient.GroupEntry{
		V: 1, Group: l.ID, Seq: l.HeadSeq + 1, Prev: l.HeadHash, Type: "rotate",
		Author: b64pub(alice), Epoch: l.Epoch + 1,
		Envelopes: []testclient.GroupEnvelope{{Recipient: b64pub(alice), HPKECiphertext: "Y3Q"}},
		KeyChain:  "a2M", Ts: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	_, err = bob.AcceptInvite(ctx, inv2.inviteID, entry)
	requireAPIError(t, err, "invite_not_usable")
}

func TestLinkInviteRejectsForgeries(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	l := createList(t, ctx, alice)
	inv, l := openLinkInvite(t, ctx, alice, l)

	// A proof not signed by the invite key: whoever has only the invite id
	// (the server included) can't admit anyone.
	_, wrongKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, err = bob.AcceptInvite(ctx, inv.inviteID, joinEntry(t, ctx, bob, inv, wrongKey))
	requireAPIError(t, err, "forbidden")

	// A join sent straight to the list's log: non-members can't append.
	_, err = bob.AppendLog(ctx, l.ID, joinEntry(t, ctx, bob, inv, inv.priv))
	requireAPIError(t, err, "forbidden")

	// A package naming an invite key with no open `invite` entry.
	otherPub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, otherID, _ := testInviteSecret(t)
	_, err = alice.CreateLinkInvite(ctx, l.ID, otherID, []byte("pkg"), time.Now().Add(time.Hour), otherPub)
	requireAPIError(t, err, "conflict")

	// Only the owner opens invites.
	_, err = alice.AppendLog(ctx, l.ID, addEntry(l, alice, bob))
	require.NoError(t, err)
	l2 := sharedLists(t, ctx, bob)
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, err = bob.AppendLog(ctx, l.ID, testclient.GroupEntry{
		V: 1, Group: l.ID, Seq: l2[0].HeadSeq + 1, Prev: l2[0].HeadHash, Type: "invite",
		Author: b64pub(bob), Epoch: l2[0].Epoch, Invite: b64raw(pub), Expires: time.Now().Add(time.Hour).UnixMilli(),
		Ts: time.Now().UnixMilli(),
	})
	requireAPIError(t, err, "forbidden")

	// The original invite is still open and still works for someone else.
	carol := newClient(t, baseURL)
	require.NoError(t, carol.Signup(ctx, []byte("carol-bundle")))
	_, err = carol.AcceptInvite(ctx, inv.inviteID, joinEntry(t, ctx, carol, inv, inv.priv))
	require.NoError(t, err)
}

// groupIDOf is the list id: hex SHA-256 of the genesis entry, which the
// invite log's first hash already is (in base64url).
func groupIDOf(t *testing.T, genesis testclient.LogEntryView) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(genesis.Hash)
	require.NoError(t, err)
	return hex.EncodeToString(raw)
}

// epochAt reads the current epoch from the newest entry.
func epochAt(t *testing.T, log []testclient.LogEntryView) int64 {
	t.Helper()
	var e struct {
		Epoch int64 `json:"epoch"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(log[len(log)-1].Entry)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &e))
	return e.Epoch
}
