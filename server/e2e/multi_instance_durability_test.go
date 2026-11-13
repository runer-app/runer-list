package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"runer-api/internal/testclient"

	"github.com/stretchr/testify/require"
)

// syncer is a client that syncs the way the app does (Sync §2-4): it keeps
// the highest seq it holds as `since` and an outbox of unacked edits, and on
// every (re)connect re-subscribes from `since` and resends the outbox.
type syncer struct {
	t      *testing.T
	name   string
	client *testclient.Client
	list   testclient.GroupView

	mu       gosync.Mutex
	conn     *testclient.SyncConn
	synced   bool
	since    int64
	have     map[int64]string // seq -> ct, everything received
	outbox   map[string]string
	acked    map[string]int64 // ct -> seq
	written  int
	connects int
}

func newSyncer(t *testing.T, name string, c *testclient.Client, l testclient.GroupView) *syncer {
	return &syncer{t: t, name: name, client: c, list: l,
		have: map[int64]string{}, outbox: map[string]string{}, acked: map[string]int64{}}
}

// run keeps the syncer connected until ctx is done.
func (s *syncer) run(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := s.client.DialSync(ctx)
		if err != nil { // an instance restarting (502) or not listening yet (503)
			time.Sleep(20 * time.Millisecond)
			continue
		}
		s.mu.Lock()
		s.conn, s.synced = conn, false
		s.connects++
		since := s.since
		s.mu.Unlock()
		if err := conn.Sub(ctx, s.list.ID, since); err == nil {
			s.read(ctx, conn)
		}
		_ = conn.Close()
		s.mu.Lock()
		s.conn = nil
		s.mu.Unlock()
	}
}

func (s *syncer) read(ctx context.Context, conn *testclient.SyncConn) {
	for {
		f, err := conn.Recv(ctx)
		if err != nil {
			return
		}
		s.mu.Lock()
		switch f["t"] {
		case "update":
			seq := int64(f["seq"].(float64))
			s.have[seq] = f["msg"].(map[string]any)["ct"].(string)
			s.since = max(s.since, seq)
		case "synced":
			s.synced = true
			s.since = max(s.since, int64(f["head"].(float64)))
			// Like the app: the outbox goes out once the list is synced.
			for cid, ct := range s.outbox {
				_ = conn.SendUpdate(ctx, cid, s.header(), ct)
			}
		case "ack":
			seq := int64(f["seq"].(float64))
			cid := f["cid"].(string)
			if ct, ok := s.outbox[cid]; ok {
				delete(s.outbox, cid)
				s.acked[ct] = seq
				s.have[seq] = ct
			}
			if s.synced {
				s.since = max(s.since, seq) // Sync §3, ordering per connection
			}
			// Now and then reconnect right after an ack: if an earlier
			// update were still on its way, `since` would skip it for good.
			if s.written%7 == 0 {
				_ = conn.Close()
			}
		case "error":
			if f["code"] != "rate-limited" {
				s.t.Errorf("%s: error frame %v", s.name, f)
			}
		}
		s.mu.Unlock()
	}
}

func (s *syncer) header() testclient.UpdateHeader {
	return s.client.UpdateHeader(s.list.ID, s.list.Epoch, "")
}

// write queues one edit and sends it if connected and synced; otherwise it
// goes out with the outbox on the next connect.
func (s *syncer) write(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written++
	cid := fmt.Sprintf("%s-%d", s.name, s.written)
	// The content starts with the cid, so it is unique. Every third edit is
	// too large for a notification payload: instances read it back, which
	// slows delivery the way a busy instance would.
	ct := cid
	if s.written%3 == 0 {
		ct += "|" + strings.Repeat("x", 10_000)
	}
	s.outbox[cid] = ct
	if s.conn != nil && s.synced {
		_ = s.conn.SendUpdate(ctx, cid, s.header(), ct)
	}
}

// content is the i-th edit's content (see write).
func (s *syncer) content(i int) string {
	ct := fmt.Sprintf("%s-%d", s.name, i)
	if i%3 == 0 {
		ct += "|" + strings.Repeat("x", 10_000)
	}
	return ct
}

// drop closes the client's socket from its side, like a network change.
func (s *syncer) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// stored reads the list's updates from the database: seq -> ct.
func stored(t *testing.T, ctx context.Context, list string) map[int64]string {
	t.Helper()
	rows, err := shared.pool.Query(ctx, `SELECT seq, msg FROM list_updates WHERE list_id = $1`, list)
	require.NoError(t, err)
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var seq int64
		var msg []byte
		require.NoError(t, rows.Scan(&seq, &msg))
		var m struct {
			Ct string `json:"ct"`
		}
		require.NoError(t, json.Unmarshal(msg, &m))
		out[seq] = m.Ct
	}
	require.NoError(t, rows.Err())
	return out
}

// Users must not lose an edit, however the deployment misbehaves. Four
// members edit one list at once through a two-instance cluster while
// instances restart, listener connections are killed and client sockets
// drop. Afterwards every edit is stored, under dense seqs, every ack names
// the stored seq, and every client holds exactly what the database holds,
// with nothing missed live and nothing skipped at catch-up.
func TestBehindLoadBalancerNoEditIsLostUnderChaos(t *testing.T) {
	c := newCluster(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	owner := newClient(t, c.URL)
	require.NoError(t, owner.Signup(ctx, []byte("owner-bundle")))
	l := createList(t, ctx, owner)
	clients := []*testclient.Client{owner}
	for i := 0; i < 3; i++ {
		m := newClient(t, c.URL)
		require.NoError(t, m.Signup(ctx, []byte(fmt.Sprintf("member-%d", i))))
		l = addMember(t, ctx, owner, l, m)
		clients = append(clients, m)
	}

	runCtx, stopRun := context.WithCancel(ctx)
	var running gosync.WaitGroup
	syncers := make([]*syncer, len(clients))
	for i, cl := range clients {
		syncers[i] = newSyncer(t, fmt.Sprintf("c%d", i), cl, l)
		running.Add(1)
		go func() { defer running.Done(); syncers[i].run(runCtx) }()
	}

	// Writers: about 30 edits a second each, for a few seconds.
	writeCtx, stopWriting := context.WithCancel(ctx)
	var writing gosync.WaitGroup
	for _, s := range syncers {
		writing.Add(1)
		go func() {
			defer writing.Done()
			for writeCtx.Err() == nil {
				s.write(ctx)
				time.Sleep(30 * time.Millisecond)
			}
		}()
	}

	// Chaos, one disruption every 300 ms.
	chaos := []func(){
		func() { c.restart(0) },
		func() {
			_, err := shared.pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = 'runer-listener'`)
			require.NoError(t, err)
		},
		func() { syncers[1].drop(); syncers[2].drop() },
		func() { c.restart(1) },
		func() { syncers[0].drop(); syncers[3].drop() },
	}
	for round := 0; round < 3; round++ {
		for _, disrupt := range chaos {
			time.Sleep(300 * time.Millisecond)
			disrupt()
		}
	}
	time.Sleep(300 * time.Millisecond)
	stopWriting()
	writing.Wait()

	// Calm: every outbox drains, and every client catches up live.
	db := map[int64]string{}
	require.Eventually(t, func() bool {
		for _, s := range syncers {
			s.mu.Lock()
			pending := len(s.outbox)
			s.mu.Unlock()
			if pending > 0 {
				return false
			}
		}
		db = stored(t, ctx, l.ID)
		for _, s := range syncers {
			s.mu.Lock()
			n := len(s.have)
			s.mu.Unlock()
			if n < len(db) {
				return false
			}
		}
		return true
	}, 20*time.Second, 50*time.Millisecond, "outboxes drain and clients catch up")
	stopRun()
	running.Wait()

	// Dense seqs.
	for seq := int64(1); seq <= int64(len(db)); seq++ {
		require.Contains(t, db, seq, "seqs are dense")
	}
	// Every edit is stored; an edit resent after a lost ack may be stored
	// twice (updates are idempotent), never zero times.
	storedCts := map[string]bool{}
	for _, ct := range db {
		storedCts[ct] = true
	}
	total, connects := 0, 0
	for _, s := range syncers {
		total += s.written
		connects += s.connects
		for i := 1; i <= s.written; i++ {
			require.True(t, storedCts[s.content(i)], "%s-%d is stored", s.name, i)
		}
		for ct, seq := range s.acked {
			require.Equal(t, ct, db[seq], "an ack names the seq its edit is stored under")
		}
		require.Equal(t, db, s.have, "%s holds exactly what the database holds", s.name)
	}
	t.Logf("%d edits, %d stored updates, %d connects across %d clients", total, len(db), connects, len(syncers))
	require.Greater(t, connects, 2*len(syncers), "the chaos really did cut connections")
}

// A snapshot uploaded through one instance while another member keeps
// writing through the other: a client catching up from scratch gets the
// snapshot and then every update after it, none missing.
func TestBehindLoadBalancerSnapshotCatchUpMissesNothing(t *testing.T) {
	c := newCluster(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	alice, bob, l := aliceAndBob(t, ctx, c)
	aliceConn := subscribed(t, ctx, alice, 0, l.ID)
	bobConn := subscribed(t, ctx, bob, 1, l.ID)

	// Bob writes through instance 1 the whole time.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 40; i++ {
			if err := bobConn.SendUpdate(ctx, fmt.Sprintf("b%d", i), bob.UpdateHeader(l.ID, l.Epoch, ""), fmt.Sprintf("bob-%d", i)); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	// Alice snapshots through instance 0 at whatever head she has seen.
	var head int64
	for head < 15 {
		f, err := aliceConn.RecvUntil(ctx, isType("update"))
		require.NoError(t, err)
		head = int64(f["seq"].(float64))
	}
	require.NoError(t, aliceConn.SendSnapshot(ctx, "snap", alice.UpdateHeader(l.ID, l.Epoch, "snapshot"), head, "state"))
	_, err := aliceConn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] == "ack" && f["cid"] == "snap" })
	require.NoError(t, err)
	<-done
	require.Eventually(t, func() bool { return len(stored(t, ctx, l.ID)) == 40 }, 5*time.Second, 20*time.Millisecond)

	// A fresh client, at either instance.
	for i := 0; i < 2; i++ {
		fresh := sameIdentityNewInstance(t, c.URL, bob.Identity)
		require.NoError(t, fresh.Login(ctx))
		pin(fresh, i)
		conn, err := fresh.DialSync(ctx)
		require.NoError(t, err)
		require.NoError(t, conn.Sub(ctx, l.ID, 0))
		// After the connect-time `group` frames, the snapshot comes first.
		f, err := conn.RecvUntil(ctx, func(f map[string]any) bool { return f["t"] != "group" })
		require.NoError(t, err)
		require.Equal(t, "snapshot", f["t"])
		require.EqualValues(t, head, f["covers_seq"])
		for seq := head + 1; seq <= 40; seq++ {
			f, err := conn.RecvUntil(ctx, isType("update"))
			require.NoError(t, err)
			require.EqualValues(t, seq, f["seq"], "every update after the snapshot, in order")
		}
		synced, err := conn.RecvUntil(ctx, isType("synced"))
		require.NoError(t, err)
		require.EqualValues(t, 40, synced["head"])
		_ = conn.Close()
	}
}
