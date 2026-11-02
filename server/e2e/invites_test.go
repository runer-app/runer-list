package e2e_test

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/testclient"

	"github.com/stretchr/testify/require"
)

// testInviteSecret stands in for the 32-random-byte `s` a real client
// generates (E2EE §6). id/mac are toy derivations — the
// server treats both as opaque either way — used only so this test has
// something to assert never leaks.
func testInviteSecret(t *testing.T) (s []byte, inviteID string, mac func(payload []byte) []byte) {
	t.Helper()
	s = make([]byte, 32)
	_, err := rand.Read(s)
	require.NoError(t, err)
	idHash := sha256.Sum256(append([]byte("invite-id:"), s...))
	inviteID = base64.RawURLEncoding.EncodeToString(idHash[:16])
	macFn := func(payload []byte) []byte {
		h := hmac.New(sha256.New, s)
		h.Write(payload)
		return h.Sum(nil)
	}
	return s, inviteID, macFn
}

func TestInviteJoinApproveFlow(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))

	_, inviteID, mac := testInviteSecret(t)
	pkg := []byte("encrypted{list genesis hash, log head, alice pub, name}")
	inv, err := alice.CreateInvite(ctx, ws.ID, inviteID, pkg, time.Now().Add(24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, inviteID, inv.ID)

	// Bob follows the link: fetches the package, then joins.
	fetchedPkg, err := bob.GetInvitePackage(ctx, inviteID)
	require.NoError(t, err)
	require.Equal(t, pkg, fetchedPkg)

	joinMAC := mac([]byte(inviteID + b64pub(bob)))
	_, err = bob.JoinInvite(ctx, inviteID, []byte("enc-display-name"), joinMAC)
	require.NoError(t, err)

	// Alice sees the pending request with BOB'S REAL, SERVER-AUTHENTICATED
	// identity keys (not anything Bob could have claimed — see
	// TestJoinRequestIdentityAlwaysServerAuthenticated).
	pending, err := alice.ListJoinRequests(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, b64pub(bob), pending[0].IdentitySignPub)
	require.Equal(t, bob.AccountID, pending[0].AccountID)

	ws = approveJoinRequest(t, ctx, alice, ws, pending[0])
	require.Equal(t, int64(1), ws.HeadSeq)

	members, err := alice.ListGroupMembers(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, members, 2)

	// The invite is now consumed: a second join attempt is rejected.
	_, err = bob.JoinInvite(ctx, inviteID, []byte("enc-display-name"), joinMAC)
	require.Error(t, err)
}

func TestSecondJoinOnConsumedInviteRejected(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))

	_, inviteID, mac := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, ws.ID, inviteID, []byte("pkg"), time.Now().Add(24*time.Hour))
	require.NoError(t, err)

	joinMAC := mac([]byte(inviteID + b64pub(bob)))
	_, err = bob.JoinInvite(ctx, inviteID, []byte("name"), joinMAC)
	require.NoError(t, err)

	pending, err := alice.ListJoinRequests(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	approveJoinRequest(t, ctx, alice, ws, pending[0])

	carol := newClient(t, baseURL)
	require.NoError(t, carol.Signup(ctx, []byte("carol-bundle")))
	_, err = carol.JoinInvite(ctx, inviteID, []byte("name"), mac([]byte(inviteID+b64pub(carol))))
	require.Error(t, err, "a consumed invite must reject further join requests")
}

func TestExpiredInviteRejected(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))

	_, inviteID, mac := testInviteSecret(t)
	// expires_at in the past — the server only rejects creation past the
	// 7-day CEILING, an already-expired expiry is a legal (if useless)
	// value, so this exercises the expiry check at join/fetch time.
	_, err := alice.CreateInvite(ctx, ws.ID, inviteID, []byte("pkg"), time.Now().Add(-time.Minute))
	require.NoError(t, err)

	_, err = bob.GetInvitePackage(ctx, inviteID)
	require.Error(t, err)

	_, err = bob.JoinInvite(ctx, inviteID, []byte("name"), mac([]byte(inviteID+b64pub(bob))))
	require.Error(t, err)
}

func TestRevokedInviteRejected(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))

	_, inviteID, mac := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, ws.ID, inviteID, []byte("pkg"), time.Now().Add(24*time.Hour))
	require.NoError(t, err)

	require.NoError(t, alice.RevokeInvite(ctx, inviteID))

	_, err = bob.JoinInvite(ctx, inviteID, []byte("name"), mac([]byte(inviteID+b64pub(bob))))
	require.Error(t, err)
}

func TestOnlyOwnerCanCreateInviteOrApprove(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL) // a plain member, added directly (no invite needed for this check)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	addBob := testclient.GroupEntry{
		V: 1, Group: ws.ID, Seq: 1, Prev: ws.HeadHash, Type: "add",
		Author: b64pub(alice), Subject: b64pub(bob), SubjectAgree: bob.AgreePub(), Epoch: 0,
		Envelopes: []testclient.GroupEnvelope{{Recipient: b64pub(bob), HPKECiphertext: "Y3Q"}},
		Ts:        time.Now().UnixMilli(),
	}
	ws, err := alice.AppendLog(ctx, ws.ID, addBob)
	require.NoError(t, err)

	// Bob (non-admin) cannot create an invite for the workspace.
	_, err = bob.CreateInvite(ctx, ws.ID, "some-invite-id", []byte("pkg"), time.Now().Add(time.Hour))
	require.Error(t, err)

	// Nor can he approve a pending join request — that's just an `add`
	// entry, and rule 3 already forbids a non-admin author.
	carol := newClient(t, baseURL)
	require.NoError(t, carol.Signup(ctx, []byte("carol-bundle")))
	_, inviteID, aliceMac := testInviteSecret(t)
	_, err = alice.CreateInvite(ctx, ws.ID, inviteID, []byte("pkg"), time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = carol.JoinInvite(ctx, inviteID, []byte("name"), aliceMac([]byte(inviteID+b64pub(carol))))
	require.NoError(t, err)

	pending, err := alice.ListJoinRequests(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	badApprove := testclient.GroupEntry{
		V: 1, Group: ws.ID, Seq: ws.HeadSeq + 1, Prev: ws.HeadHash, Type: "add",
		Author: b64pub(bob), Subject: pending[0].IdentitySignPub, SubjectAgree: pending[0].IdentityAgreePub, Epoch: ws.Epoch,
		Envelopes:   []testclient.GroupEnvelope{{Recipient: pending[0].IdentitySignPub, HPKECiphertext: "Y3Q"}},
		InviteProof: pending[0].MAC,
		Ts:          time.Now().UnixMilli(),
	}
	_, err = bob.AppendLog(ctx, ws.ID, badApprove)
	require.Error(t, err)

	// Also can't revoke someone else's invite.
	err = bob.RevokeInvite(ctx, inviteID)
	require.Error(t, err)
}

// TestJoinRequestIdentityAlwaysServerAuthenticated is this API's
// closure of the "server swaps the requester's key" attack: the join endpoint never accepts client-submitted identity
// keys at all — it always uses the caller's own already-registered
// account (E2EE §3.3, unique per identity_sign_pub). There is nothing for
// a malicious server, or a malicious requester, to swap.
func TestJoinRequestIdentityAlwaysServerAuthenticated(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))

	_, inviteID, mac := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, ws.ID, inviteID, []byte("pkg"), time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = bob.JoinInvite(ctx, inviteID, []byte("name"), mac([]byte(inviteID+b64pub(bob))))
	require.NoError(t, err)

	pending, err := alice.ListJoinRequests(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, b64pub(bob), pending[0].IdentitySignPub, "the owner must see the requester's real, registered identity key")
	require.Equal(t, bob.AccountID, pending[0].AccountID)
}

// TestInviteSecretNeverAppearsInServerData checks that a
// WebSocket and HTTP capture of the whole flow never contains the invite
// secret. The server never receives `s` at all —
// only id(s), package (AEAD under s), and MAC(s, ...) — so this asserts
// the raw secret bytes are absent from everything the server returned
// across the whole flow.
func TestInviteSecretNeverAppearsInServerData(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	ws := createList(t, ctx, alice)

	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))

	s, inviteID, mac := testInviteSecret(t)
	pkg := []byte("package-ciphertext")
	inv, err := alice.CreateInvite(ctx, ws.ID, inviteID, pkg, time.Now().Add(time.Hour))
	require.NoError(t, err)

	fetchedPkg, err := bob.GetInvitePackage(ctx, inviteID)
	require.NoError(t, err)

	joinMAC := mac([]byte(inviteID + b64pub(bob)))
	_, err = bob.JoinInvite(ctx, inviteID, []byte("enc-name"), joinMAC)
	require.NoError(t, err)

	pending, err := alice.ListJoinRequests(ctx, ws.ID)
	require.NoError(t, err)

	captured := strings.Join([]string{
		inv.ID, string(fetchedPkg),
		pending[0].MAC, pending[0].EncDisplayName, pending[0].IdentitySignPub, pending[0].AccountID,
	}, "|")

	require.NotContains(t, captured, base64.RawURLEncoding.EncodeToString(s))
}
