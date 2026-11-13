package store_test

import (
	"context"
	"testing"
	"time"

	"runer-api/internal/store"
	"runer-api/internal/store/storetest"

	"github.com/stretchr/testify/require"
)

func newChallenges(t *testing.T) (*store.Challenges, *store.Challenges) {
	t.Helper()
	ctx := context.Background()
	url := storetest.StartPostgres(t)
	a, err := store.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	b, err := store.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(b.Close)
	return store.NewChallenges(a), store.NewChallenges(b)
}

// A login challenge from one instance is answered once, at another.
// Keyed by subject and nonce: a second challenge for the same subject (an
// attacker who knows the victim's auth_pub) or a wrong nonce leaves the
// victim's pending one alone.
func TestLoginChallengesAcrossInstances(t *testing.T) {
	ctx := context.Background()
	a, b := newChallenges(t)

	victim, err := a.Issue(ctx, store.ChallengeLogin, "pub", time.Minute, false)
	require.NoError(t, err)
	attacker, err := b.Issue(ctx, store.ChallengeLogin, "pub", time.Minute, false)
	require.NoError(t, err)

	consume := func(c *store.Challenges, subject string, nonce []byte) bool {
		ok, err := c.Consume(ctx, store.ChallengeLogin, subject, nonce)
		require.NoError(t, err)
		return ok
	}
	require.False(t, consume(b, "pub", make([]byte, 32)), "a nonce never issued is rejected")
	require.False(t, consume(b, "other", victim), "bound to its subject")
	require.False(t, consume(a, "pub", victim[:31]), "not a prefix match")
	require.True(t, consume(b, "pub", victim))
	require.False(t, consume(a, "pub", victim), "single use, on every instance")
	require.True(t, consume(a, "pub", attacker))
}

func TestExpiredChallengesRejectedAndSwept(t *testing.T) {
	ctx := context.Background()
	a, b := newChallenges(t)

	abandoned, err := a.Issue(ctx, store.ChallengeLogin, "abandoned", time.Millisecond, false)
	require.NoError(t, err)
	live, err := a.Issue(ctx, store.ChallengeLogin, "live", time.Minute, false)
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)

	n, err := b.SweepExpired(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the expired challenge is swept")
	ok, err := a.Consume(ctx, store.ChallengeLogin, "abandoned", abandoned)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = b.Consume(ctx, store.ChallengeLogin, "live", live)
	require.NoError(t, err)
	require.True(t, ok, "an unexpired challenge survives the sweep")

	expired, err := a.Issue(ctx, store.ChallengeLogin, "late", time.Millisecond, false)
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	ok, err = b.Consume(ctx, store.ChallengeLogin, "late", expired)
	require.NoError(t, err)
	require.False(t, ok, "expired but not yet swept is still rejected")
}

// A rekey challenge: the newest replaces the pending one, and any attempt,
// right or wrong, uses it up.
func TestRekeyChallengesAcrossInstances(t *testing.T) {
	ctx := context.Background()
	a, b := newChallenges(t)
	consume := func(c *store.Challenges, nonce []byte) bool {
		ok, err := c.ConsumeAny(ctx, store.ChallengeRekey, "acct", nonce)
		require.NoError(t, err)
		return ok
	}

	first, err := a.Issue(ctx, store.ChallengeRekey, "acct", time.Minute, true)
	require.NoError(t, err)
	second, err := b.Issue(ctx, store.ChallengeRekey, "acct", time.Minute, true)
	require.NoError(t, err)
	require.False(t, consume(a, first), "replaced by the newer challenge")
	require.False(t, consume(a, second), "the failed attempt used it up")

	third, err := b.Issue(ctx, store.ChallengeRekey, "acct", time.Minute, true)
	require.NoError(t, err)
	require.True(t, consume(a, third))
	require.False(t, consume(b, third), "single use")
}
