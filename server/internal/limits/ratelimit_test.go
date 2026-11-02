package limits

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKeyed_SweepKeepsExhaustedDropsIdle(t *testing.T) {
	k := NewKeyed(1000, 1) // refills a token in 1ms
	require.True(t, k.Allow("idle"))
	time.Sleep(5 * time.Millisecond) // "idle" is back to full burst

	slow := PerHour(1)
	require.True(t, slow.Allow("blocked"))

	// Force the next Allow to sweep.
	k.lastSweep, slow.lastSweep = time.Time{}, time.Time{}

	require.True(t, k.Allow("other"))
	require.NotContains(t, k.limiters, "idle", "a full bucket is dropped")

	require.False(t, slow.Allow("blocked"), "an exhausted bucket survives the sweep and still limits")
}
