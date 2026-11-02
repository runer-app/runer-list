package e2e_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/retention"
	"runer-api/internal/testclient"

	"github.com/stretchr/testify/require"
)

// inboxView is c's Inbox as GET /groups lists it.
func inboxView(t *testing.T, ctx context.Context, c *testclient.Client) testclient.GroupView {
	t.Helper()
	lists, err := c.ListGroups(ctx)
	require.NoError(t, err)
	for _, l := range lists {
		if l.ID == c.InboxID {
			return l
		}
	}
	t.Fatalf("inbox %q not listed", c.InboxID)
	return testclient.GroupView{}
}

// Sign-up needs a valid Inbox genesis by the identity being registered;
// without one no account is created.
func TestInbox_SignupRequiresInbox(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	c := newClient(t, baseURL)

	requireAPIError(t, c.SignupWithInbox(ctx, []byte("b"), nil, nil), "bad_request")

	other, err := testclient.NewIdentity()
	require.NoError(t, err)
	raw, sig, err := testclient.InboxGenesis(other)
	require.NoError(t, err)
	requireAPIError(t, c.SignupWithInbox(ctx, []byte("b"), raw, sig), "bad_request")

	requireAPIError(t, c.Login(ctx), "invalid_credentials")
	require.NoError(t, c.Signup(ctx, []byte("b")))
}

// Every login and GET /account name the Inbox, a list the account owns
// alone.
func TestInbox_ReturnedAtLoginAndAccount(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	c := newClient(t, baseURL)
	require.NoError(t, c.Signup(ctx, []byte("b")))
	inbox := c.InboxID
	require.NotEmpty(t, inbox)

	c.InboxID = ""
	require.NoError(t, c.Login(ctx))
	require.Equal(t, inbox, c.InboxID)

	acc, err := c.GetAccount(ctx)
	require.NoError(t, err)
	require.Equal(t, inbox, acc.InboxID)

	members, err := c.ListGroupMembers(ctx, inbox)
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Equal(t, "owner", members[0].Role)
}

// The Inbox can't be deleted or shared: no add, no invite.
func TestInbox_NotDeletableOrShareable(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	alice, bob := newClient(t, baseURL), newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("a")))
	require.NoError(t, bob.Signup(ctx, []byte("b")))
	inbox := inboxView(t, ctx, alice)

	requireAPIError(t, alice.DeleteGroup(ctx, inbox.ID), "forbidden")

	_, err := alice.AppendLog(ctx, inbox.ID, addEntry(inbox, alice, bob))
	requireAPIError(t, err, "forbidden")

	invPub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, err = alice.AppendLog(ctx, inbox.ID, testclient.GroupEntry{
		V: 1, Group: inbox.ID, Seq: inbox.HeadSeq + 1, Prev: inbox.HeadHash, Type: "invite",
		Author: b64pub(alice), Epoch: inbox.Epoch, Invite: b64raw(invPub), Expires: time.Now().Add(time.Hour).UnixMilli(),
		Ts: time.Now().UnixMilli(),
	})
	requireAPIError(t, err, "forbidden")
}

// Account deletion removes the Inbox, and the retention job can purge it.
func TestInbox_RemovedWithAccount(t *testing.T) {
	baseURL, pool := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	c := newClient(t, baseURL)
	require.NoError(t, c.Signup(ctx, []byte("b")))
	require.NoError(t, c.DeleteAccount(ctx))

	var deleted bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM groups WHERE id = $1`, c.InboxID).Scan(&deleted))
	require.True(t, deleted)

	purger := retention.NewPurger(pool, fakeClock{now: time.Now().Add(retention.DefaultWindow + time.Hour)}, retention.DefaultWindow)
	report, err := purger.PurgeOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, report.Groups)
	require.Equal(t, 1, report.Accounts)
}
