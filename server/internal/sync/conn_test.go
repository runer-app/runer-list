package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// pair returns a server-side Conn and the client end of the same socket.
func pair(t *testing.T) (*Conn, *websocket.Conn) {
	t.Helper()
	ready := make(chan *Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c := newConn(ws, "acct", "sess")
		ready <- c
		// Keep reading so control frames (pong, close) are processed.
		for {
			if _, _, err := ws.Read(context.Background()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	client.SetReadLimit(-1)
	t.Cleanup(func() { _ = client.CloseNow() })
	return <-ready, client
}

func frame(t *testing.T, seq int64) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"seq": seq})
	require.NoError(t, err)
	return b
}

func readSeq(t *testing.T, client *websocket.Conn) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := client.Read(ctx)
	require.NoError(t, err)
	var f struct{ Seq int64 }
	require.NoError(t, json.Unmarshal(data, &f))
	return f.Seq
}

func requireClosedSoon(t *testing.T, c *Conn) {
	t.Helper()
	require.Eventually(t, c.isClosed, 5*time.Second, 10*time.Millisecond)
}

// Fan-out to a peer that stops reading never blocks the caller; once
// the queue is full the peer is disconnected.
func TestConn_SlowConsumerDisconnectedWithoutBlocking(t *testing.T) {
	c, _ := pair(t) // client never reads
	big := make([]byte, 1<<20)

	start := time.Now()
	for range 500 {
		c.deliver("k", liveFrame{seq: 1, data: big})
	}
	require.Less(t, time.Since(start), time.Second, "deliver must never wait on the peer")
	requireClosedSoon(t, c)
}

// Live frames that arrive during catch-up are held, then released in
// order after the history, minus any the history already contained.
func TestConn_HeldFramesReleasedAfterCatchUp(t *testing.T) {
	c, client := pair(t)
	ctx := context.Background()

	c.beginCatchUp("k")
	c.deliver("k", liveFrame{seq: 3, data: frame(t, 3)})
	c.deliver("k", liveFrame{seq: 4, data: frame(t, 4)})
	c.deliver("k", liveFrame{seq: 5, data: frame(t, 5)})
	for _, seq := range []int64{1, 2, 3, 4} { // history, read after the live frames arrived
		require.NoError(t, c.send(ctx, map[string]any{"seq": seq}))
	}
	require.NoError(t, c.finishCatchUp(ctx, "k", 4))
	c.deliver("k", liveFrame{seq: 6, data: frame(t, 6)})

	for _, want := range []int64{1, 2, 3, 4, 5, 6} {
		require.Equal(t, want, readSeq(t, client))
	}
}

// A long catch-up on one page must not get the connection dropped as a
// slow consumer when live frames for another page arrive meanwhile: own
// sends leave queue room for fan-out.
func TestConn_CatchUpLeavesRoomForLiveFrames(t *testing.T) {
	c, _ := pair(t) // client never reads, so the queue backs up
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	big := make([]byte, 64<<10)

	go func() { // catch-up: far more history than the queue holds
		for range 1000 {
			if c.sendRaw(ctx, big) != nil {
				return
			}
		}
	}()
	require.Eventually(t, func() bool { return len(c.out) >= ownQueueLen }, 5*time.Second, time.Millisecond)
	time.Sleep(100 * time.Millisecond) // let the sender fill all it's allowed to

	for i := range 100 { // well within the room left for fan-out
		c.deliver("other", liveFrame{seq: int64(i + 1), data: big})
	}
	require.False(t, c.isClosed(), "live frames during catch-up must not trip the slow-consumer cut-off")
}

// A catch-up that falls too far behind live traffic disconnects
// instead of buffering without bound.
func TestConn_HeldFramesBounded(t *testing.T) {
	c, _ := pair(t)
	c.beginCatchUp("k")
	for i := range maxHeldLive + 1 {
		c.deliver("k", liveFrame{seq: int64(i + 1), data: []byte("{}")})
	}
	requireClosedSoon(t, c)
}

// Sync §2 heartbeat: a peer that never answers pings is closed.
func TestKeepAlive_ClosesWhenPongMissing(t *testing.T) {
	c, _ := pair(t) // client never reads, so never answers pings
	go keepAlive(context.Background(), c, 20*time.Millisecond, 50*time.Millisecond, func() bool { return true })
	requireClosedSoon(t, c)
}

// A listening-only socket is closed once its session ends, with a bye.
func TestKeepAlive_ClosesWhenSessionEnds(t *testing.T) {
	c, client := pair(t)
	ctx := context.Background()

	go keepAlive(ctx, c, 20*time.Millisecond, time.Second, func() bool { return false })

	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, data, err := client.Read(rctx) // also answers pings while waiting
	require.NoError(t, err)
	require.JSONEq(t, `{"t":"bye","reason":"session-revoked"}`, string(data))
	requireClosedSoon(t, c)
}
