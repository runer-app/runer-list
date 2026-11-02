package e2e_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/testclient"

	"github.com/stretchr/testify/require"
)

func b64raw(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestListLifecycle(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	alice := newClient(t, baseURL)
	require.NoError(t, alice.Signup(ctx, []byte("alice-bundle")))
	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))

	genesis := testclient.GroupEntry{V: 1, Seq: 0, Type: "create", Author: b64pub(alice), AuthorAgree: alice.AgreePub(), Epoch: 0, Ts: time.Now().UnixMilli()}
	genesisRaw, _, err := alice.SignGroupEntry(genesis)
	require.NoError(t, err)
	ws, err := alice.CreateGroup(ctx, genesis)
	require.NoError(t, err)
	require.Equal(t, int64(0), ws.HeadSeq)
	require.Equal(t, testclient.HashEntry(genesisRaw), ws.HeadHash)

	add := testclient.GroupEntry{
		V: 1, Group: ws.ID, Seq: 1, Prev: ws.HeadHash, Type: "add",
		Author: b64pub(alice), Subject: b64pub(bob), SubjectAgree: bob.AgreePub(), Epoch: 0,
		Envelopes: []testclient.GroupEnvelope{{Recipient: b64pub(bob), HPKECiphertext: "Y3Q"}},
		Ts:        time.Now().UnixMilli(),
	}
	ws, err = alice.AppendLog(ctx, ws.ID, add)
	require.NoError(t, err)
	require.Equal(t, int64(1), ws.HeadSeq)
	require.Equal(t, int64(0), ws.Epoch)

	members, err := alice.ListGroupMembers(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, members, 2)

	// Bob can now see the group too.
	bobGroups := sharedLists(t, ctx, bob)
	require.Len(t, bobGroups, 1)
	require.Equal(t, ws.ID, bobGroups[0].ID)

	remove := testclient.GroupEntry{
		V: 1, Group: ws.ID, Seq: 2, Prev: ws.HeadHash, Type: "remove",
		Author: b64pub(alice), Subject: b64pub(bob), Epoch: 1,
		Envelopes: []testclient.GroupEnvelope{{Recipient: b64pub(alice), HPKECiphertext: "Y3Q"}},
		KeyChain:  "a2V5Y2hhaW4",
		Ts:        time.Now().UnixMilli(),
	}
	ws, err = alice.AppendLog(ctx, ws.ID, remove)
	require.NoError(t, err)
	require.Equal(t, int64(2), ws.HeadSeq)
	require.Equal(t, int64(1), ws.Epoch)

	members, err = alice.ListGroupMembers(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)

	log, err := alice.GetLog(ctx, ws.ID)
	require.NoError(t, err)
	require.Len(t, log, 3)
	require.Equal(t, []int64{0, 1, 2}, []int64{log[0].Seq, log[1].Seq, log[2].Seq})
}

// The owner on two devices appends against the same head: one wins, the
// other gets 409 and succeeds on retry against the new head.
func TestConcurrentAppendConflictThenRetry(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	laptop := newClient(t, baseURL)
	require.NoError(t, laptop.Signup(ctx, []byte("alice-bundle")))
	phone := sameIdentityNewInstance(t, baseURL, laptop.Identity)
	require.NoError(t, phone.Login(ctx))
	bob := newClient(t, baseURL)
	require.NoError(t, bob.Signup(ctx, []byte("bob-bundle")))
	carol := newClient(t, baseURL)
	require.NoError(t, carol.Signup(ctx, []byte("carol-bundle")))

	l := createList(t, ctx, laptop)

	after, err := laptop.AppendLog(ctx, l.ID, addEntry(l, laptop, bob))
	require.NoError(t, err)
	require.Equal(t, int64(1), after.HeadSeq)

	// The phone built its entry against the head it saw before.
	_, err = phone.AppendLog(ctx, l.ID, addEntry(l, phone, carol))
	requireAPIError(t, err, "conflict")

	final, err := phone.AppendLog(ctx, l.ID, addEntry(after, phone, carol))
	require.NoError(t, err)
	require.Equal(t, int64(2), final.HeadSeq)

	// Adding someone twice is a conflict too, whatever the head.
	_, err = phone.AppendLog(ctx, l.ID, addEntry(final, phone, bob))
	requireAPIError(t, err, "conflict")
}

// TestServerRejectsEveryInvalidFixtureOverHTTP checks that the
// server rejects every invalid fixture over HTTP. It replays every protocol/log-fixtures/*.json file's entries against the
// real running server, using accounts whose identity keys deterministically
// match the fixtures' authors (see fixtureIdentity — must derive the same
// keys as internal/grouplog/fixtures_gen_test.go's fixtureKey).
func TestServerRejectsEveryInvalidFixtureOverHTTP(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	// Each entry is submitted from its author's own session, as the server
	// requires (an entry's author must be the caller).
	byAuthor := map[string]*testclient.Client{}
	var alice *testclient.Client
	for _, label := range []string{"alice", "bob", "carol"} {
		c := fixtureClient(t, baseURL, label)
		require.NoError(t, c.Signup(ctx, []byte(label+"-bundle")))
		byAuthor[b64pub(c)] = c
		if label == "alice" {
			alice = c
		}
	}
	submitterOf := func(raw []byte) *testclient.Client {
		var e struct {
			Author string `json:"author"`
		}
		_ = json.Unmarshal(raw, &e)
		if c, ok := byAuthor[e.Author]; ok {
			return c
		}
		return alice // unparseable entry: any session sees it rejected
	}

	dir := fixturesDir(t)
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, de := range files {
		if !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		t.Run(de.Name(), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, de.Name()))
			require.NoError(t, err)
			var f fixtureFile
			require.NoError(t, json.Unmarshal(data, &f))
			require.NotEmpty(t, f.Entries)
			if hasLinkInvite(f) {
				// Fixed past timestamps: the server's clock check refuses
				// these invites, and joins go through /invites/{id}/accept.
				// link_invites_test.go covers the server path.
				t.Skip("link-invite fixture: validator-only")
			}

			var groupID string
			var lastErr error
			for i, entry := range f.Entries {
				isLast := i == len(f.Entries)-1
				var view testclient.GroupView
				if i == 0 {
					view, lastErr = submitterOf(entry.Raw).CreateGroupRaw(ctx, entry.Raw, entry.Sig)
					if lastErr == nil {
						groupID = view.ID
					}
				} else {
					view, lastErr = submitterOf(entry.Raw).AppendLogRaw(ctx, groupID, entry.Raw, entry.Sig)
				}
				if !isLast {
					require.NoErrorf(t, lastErr, "setup entry %d over HTTP must succeed", i)
				}
			}

			if f.Valid {
				require.NoError(t, lastErr)
			} else {
				require.Error(t, lastErr, "invalid fixture must be rejected over HTTP")
			}
		})
	}
}

func b64pub(c *testclient.Client) string {
	return b64raw(c.Identity.SignPub)
}

// fixtureClient builds a client whose identity keys deterministically match
// internal/grouplog/fixtures_gen_test.go's fixtureKey and agreeKey, so
// fixture entries "authored" by e.g. "alice" resolve to a real account here.
func hasLinkInvite(f fixtureFile) bool {
	for _, e := range f.Entries {
		var w struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(e.Raw, &w)
		if w.Type == "invite" || w.Type == "uninvite" || w.Type == "join" {
			return true
		}
	}
	return false
}

func fixtureClient(t *testing.T, baseURL, label string) *testclient.Client {
	t.Helper()
	id, err := testclient.NewIdentity()
	require.NoError(t, err)
	seed := sha256.Sum256([]byte("grouplog-fixture:" + label))
	priv := ed25519.NewKeyFromSeed(seed[:])
	id.SignPub = priv.Public().(ed25519.PublicKey)
	id.SignPriv = priv
	agree := sha256.Sum256([]byte("grouplog-fixture-agree:" + label)) // fixtures_gen_test.go's agreeKey
	id.AgreePub = agree[:]
	c, err := testclient.New(baseURL, testOrigin, id)
	require.NoError(t, err)
	return c
}

type fixtureFileEntry struct {
	Raw []byte `json:"raw"`
	Sig []byte `json:"sig"`
}

type fixtureFile struct {
	Name    string             `json:"name"`
	Valid   bool               `json:"valid"`
	Entries []fixtureFileEntry `json:"entries"`
}

func fixturesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "protocol", "log-fixtures"))
	require.NoError(t, err)
	return dir
}
