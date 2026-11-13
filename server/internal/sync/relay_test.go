package sync

import (
	"context"
	"testing"
	"time"

	"runer-api/internal/store"
	"runer-api/internal/store/storetest"

	"github.com/stretchr/testify/require"
)

// A listener with nothing to hear for longer than its ping interval keeps
// its connection (a timed-out wait must not count as a loss) and still
// hears what comes after.
func TestRelayStaysListeningWhenIdle(t *testing.T) {
	ctx := t.Context()
	pool, err := store.Open(ctx, storetest.StartPostgres(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	hub := newHub(10)
	r := newRelay(pool, store.NewUpdates(pool), hub)
	r.pingEvery = 20 * time.Millisecond
	require.NoError(t, r.start(ctx))

	w := r.expect("list", 7)
	time.Sleep(200 * time.Millisecond) // ten ping intervals
	require.True(t, hub.isListening(), "an idle listener is not a lost one")

	require.NoError(t, r.publish(context.Background(), event{Kind: "u", List: "list", Seq: 7}))
	select {
	case <-w.done:
		require.True(t, w.ok)
	case <-time.After(5 * time.Second):
		t.Fatal("notification not heard after idling")
	}

	var listeners int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name = $1`, listenerAppName).Scan(&listeners))
	require.Equal(t, 1, listeners, "the same listener connection, never reconnected")
}
