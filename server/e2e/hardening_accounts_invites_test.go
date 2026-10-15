package e2e_test

// Regression tests for security hardening of sign-up and invites.

import (
	"context"
	"testing"
	"time"

	"runer-api/internal/auth"

	"github.com/stretchr/testify/require"
)

// Signup must prove possession of the identity key.
func TestIdentityKeyProof(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	// Claiming someone else's identity key without its private half.
	victim := newClient(t, baseURL)
	require.NoError(t, victim.Signup(ctx, []byte("v")))
	claimer := newClient(t, baseURL)
	claimer.Identity.SignPub = victim.Identity.SignPub
	requireAPIError(t, claimer.Signup(ctx, []byte("c")), "invalid_credentials")
}

// Deleting a list revokes its invites.
func TestListDeletionRevokesInvites(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, outsider := newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, outsider.Signup(ctx, []byte("o")))
	ws := createList(t, ctx, alice)
	_, inviteID, mac := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, ws.ID, inviteID, []byte("pkg"), time.Now().Add(time.Hour))
	require.NoError(t, err)

	require.NoError(t, alice.DeleteGroup(ctx, ws.ID))

	anon := newClient(t, baseURL)
	_, err = anon.GetInvitePackage(ctx, inviteID)
	require.Error(t, err)
	_, err = outsider.JoinInvite(ctx, inviteID, []byte("name"), mac([]byte("x")))
	require.Error(t, err)
}

// One account holds at most one pending request per invite; a repeat
// replaces it instead of using up the invite's budget with duplicates.
func TestOnePendingJoinRequestPerAccount(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob := newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	ws := createList(t, ctx, alice)
	_, inviteID, mac := testInviteSecret(t)
	_, err := alice.CreateInvite(ctx, ws.ID, inviteID, []byte("pkg"), time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = bob.JoinInvite(ctx, inviteID, []byte("first"), mac([]byte("1")))
	require.NoError(t, err)
	_, err = bob.JoinInvite(ctx, inviteID, []byte("second"), mac([]byte("2")))
	require.NoError(t, err)

	reqs, err := alice.ListJoinRequests(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	require.Equal(t, b64raw(mac([]byte("2"))), reqs[0].MAC)
}
