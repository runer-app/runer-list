package e2e_test

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"runer-api/internal/testclient"

	"github.com/stretchr/testify/require"
)

// createList makes owner the sole member of a fresh list.
func createList(t *testing.T, ctx context.Context, owner *testclient.Client) testclient.GroupView {
	t.Helper()
	genesis := testclient.GroupEntry{V: 1, Seq: 0, Type: "create", Author: b64pub(owner), Epoch: 0, Ts: time.Now().UnixMilli()}
	l, err := owner.CreateGroup(ctx, genesis)
	require.NoError(t, err)
	return l
}

// addEntry is the `add` entry admitting subject to l at its current head.
func addEntry(l testclient.GroupView, owner, subject *testclient.Client) testclient.GroupEntry {
	return testclient.GroupEntry{
		V: 1, Group: l.ID, Seq: l.HeadSeq + 1, Prev: l.HeadHash, Type: "add",
		Author: b64pub(owner), Subject: b64pub(subject), SubjectAgree: subject.AgreePub(), Epoch: l.Epoch,
		Envelopes: []testclient.GroupEnvelope{{Recipient: b64pub(subject), HPKECiphertext: "Y3Q"}},
		Ts:        time.Now().UnixMilli(),
	}
}

// addMember appends a plain `add` entry (no invite).
func addMember(t *testing.T, ctx context.Context, owner *testclient.Client, l testclient.GroupView, subject *testclient.Client) testclient.GroupView {
	t.Helper()
	updated, err := owner.AppendLog(ctx, l.ID, addEntry(l, owner, subject))
	require.NoError(t, err)
	return updated
}

// approveJoinRequest appends the `add` entry that admits a joined account,
// carrying invite_proof = the MAC the join request relayed (E2EE §4.1,
// §6: "An invite is consumed when the add entry referencing it is
// appended").
func approveJoinRequest(t *testing.T, ctx context.Context, owner *testclient.Client, l testclient.GroupView, jr testclient.JoinRequestView) testclient.GroupView {
	t.Helper()
	mac, err := base64.RawURLEncoding.DecodeString(jr.MAC)
	require.NoError(t, err)
	add := testclient.GroupEntry{
		V: 1, Group: l.ID, Seq: l.HeadSeq + 1, Prev: l.HeadHash, Type: "add",
		Author: b64pub(owner), Subject: jr.IdentitySignPub, SubjectAgree: jr.IdentityAgreePub, Epoch: l.Epoch,
		Envelopes:   []testclient.GroupEnvelope{{Recipient: jr.IdentitySignPub, HPKECiphertext: "Y3Q"}},
		InviteProof: base64.RawURLEncoding.EncodeToString(mac),
		Ts:          time.Now().UnixMilli(),
	}
	updated, err := owner.AppendLog(ctx, l.ID, add)
	require.NoError(t, err)
	return updated
}

func requireAPIError(t *testing.T, err error, code string) {
	t.Helper()
	var apiErr *testclient.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, code, apiErr.Code)
}

// sharedLists is every list c can access except its own Inbox.
func sharedLists(t *testing.T, ctx context.Context, c *testclient.Client) []testclient.GroupView {
	t.Helper()
	all, err := c.ListGroups(ctx)
	require.NoError(t, err)
	var out []testclient.GroupView
	for _, l := range all {
		if l.ID != c.InboxID {
			out = append(out, l)
		}
	}
	return out
}
