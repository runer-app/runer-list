package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChallengeStore_IssueSweepsExpired(t *testing.T) {
	s := newChallengeStore(time.Millisecond)
	abandoned, err := s.issue("abandoned")
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	s.ttl = time.Minute
	s.lastSweep = time.Time{} // force the next issue to sweep
	nonce, err := s.issue("live")
	require.NoError(t, err)

	require.NotContains(t, s.entries, "abandoned"+string(abandoned))
	require.Len(t, s.entries, 1)
	require.True(t, s.consume("live", nonce), "an unexpired challenge survives the sweep")
}

// A second challenge or a wrong nonce for the same auth_pub must not
// invalidate a pending one.
func TestChallengeStore_OthersCannotEraseAChallenge(t *testing.T) {
	s := newChallengeStore(time.Minute)
	victim, err := s.issue("pub")
	require.NoError(t, err)
	attacker, err := s.issue("pub")
	require.NoError(t, err)

	require.False(t, s.consume("pub", make([]byte, 32)), "a nonce never issued is rejected")
	require.True(t, s.consume("pub", victim))
	require.False(t, s.consume("pub", victim), "single use")
	require.True(t, s.consume("pub", attacker))
}
