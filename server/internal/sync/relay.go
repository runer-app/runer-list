package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"runer-api/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Live fan-out between server instances goes through PostgreSQL
// LISTEN/NOTIFY, so it needs no service beyond the database. Every
// instance, including the author's, delivers what it hears on
// notifyChannel to its own sockets. An update is notified inside its write
// transaction, and the list row lock commits a list's updates in seq
// order; PostgreSQL delivers notifications in commit order, and one
// goroutine per instance hands them out, so live frames go out in seq
// order everywhere.
const notifyChannel = "runer_sync"

// listenerAppName names each instance's listening connection in
// pg_stat_activity.
const listenerAppName = "runer-listener"

// maxNotifyPayload keeps a payload under PostgreSQL's 8000-byte limit. An
// update too large to carry inline is read back from list_updates.
const maxNotifyPayload = 7900

// groupChunk is how many account ids one `group` notification carries.
const groupChunk = 150

// ackTimeout bounds how long a write waits to hear its own notification
// before giving up on the connection (the client reconnects and catches
// up). With a healthy listener it takes milliseconds.
const ackTimeout = 10 * time.Second

// event is one notification. Short keys keep payloads small.
type event struct {
	Kind string `json:"k"` // "u" update, "g" group, "c" close

	// "u"
	List string          `json:"l,omitempty"`
	Seq  int64           `json:"s,omitempty"`
	From string          `json:"f,omitempty"` // author connection id, never sent its own update
	Msg  json.RawMessage `json:"m,omitempty"` // absent when too large: read it back

	// "g"
	Group    string   `json:"g,omitempty"`
	Head     int64    `json:"h,omitempty"`
	Accounts []string `json:"a,omitempty"`

	// "c": close Account's connections; Session limits it to one session,
	// or with Keep to every other session.
	Account string `json:"acct,omitempty"`
	Reason  string `json:"r,omitempty"`
	Session string `json:"x,omitempty"`
	Keep    bool   `json:"keep,omitempty"`
}

// relay owns this instance's listening connection.
type relay struct {
	pool    *pgxpool.Pool
	updates *store.Updates
	hub     *Hub
	// pingEvery is how long the listener waits for a notification before
	// checking its connection is still there.
	pingEvery time.Duration

	mu      sync.Mutex
	waiters map[waitKey]*waiter
}

type waitKey struct {
	list string
	seq  int64
}

// waiter is closed once this instance has delivered the update; ok says
// whether it was (false: the listener lost its connection first).
type waiter struct {
	done chan struct{}
	ok   bool
}

func newRelay(pool *pgxpool.Pool, updates *store.Updates, hub *Hub) *relay {
	return &relay{pool: pool, updates: updates, hub: hub, pingEvery: 30 * time.Second, waiters: map[waitKey]*waiter{}}
}

// start listens, then keeps listening in the background until ctx is
// done. It returns once the first LISTEN is in place, so no socket is
// accepted before notifications can reach it.
func (r *relay) start(ctx context.Context) error {
	conn, err := r.listen(ctx)
	if err != nil {
		return err
	}
	r.hub.setListening(true)
	go r.run(ctx, conn)
	return nil
}

func (r *relay) listen(ctx context.Context) (*pgx.Conn, error) {
	pc, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire listener connection: %w", err)
	}
	conn := pc.Hijack() // held for good, so out of the pool
	if _, err := conn.Exec(ctx, `SET application_name = '`+listenerAppName+`'`); err != nil {
		_ = conn.Close(context.Background())
		return nil, fmt.Errorf("name listener connection: %w", err)
	}
	if _, err := conn.Exec(ctx, `LISTEN `+notifyChannel); err != nil {
		_ = conn.Close(context.Background())
		return nil, fmt.Errorf("listen: %w", err)
	}
	return conn, nil
}

func (r *relay) run(ctx context.Context, conn *pgx.Conn) {
	for {
		err := r.consume(ctx, conn)
		_ = conn.Close(context.Background())
		// Notifications sent from here on are lost to this instance: its
		// sockets can't be trusted to be complete any more. Close them;
		// clients reconnect and catch up. New ones wait for the LISTEN.
		r.lost()
		if ctx.Err() != nil {
			return
		}
		slog.Warn("sync listener lost its connection; closed this instance's sockets", "error", err)
		for backoff := 50 * time.Millisecond; ; backoff = min(2*backoff, 5*time.Second) {
			if conn, err = r.listen(ctx); err == nil {
				break
			}
			slog.Warn("sync listener reconnect failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}
		r.hub.setListening(true)
	}
}

// consume hands out notifications until the connection fails or ctx ends.
func (r *relay) consume(ctx context.Context, conn *pgx.Conn) error {
	for {
		wctx, cancel := context.WithTimeout(ctx, r.pingEvery)
		n, err := conn.WaitForNotification(wctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) && !conn.IsClosed() {
				// Quiet for a while: make sure the connection is still
				// there, since a dead one would wait forever.
				if err := conn.Ping(ctx); err != nil {
					return err
				}
				continue
			}
			return err
		}
		var e event
		if err := json.Unmarshal([]byte(n.Payload), &e); err != nil {
			slog.Error("malformed sync notification", "error", err)
			continue
		}
		r.handle(ctx, e)
	}
}

func (r *relay) handle(ctx context.Context, e event) {
	switch e.Kind {
	case "u":
		r.deliverUpdate(ctx, e)
	case "g":
		r.hub.sendToAccounts(e.Accounts, map[string]any{"t": "group", "group": e.Group, "head_seq": e.Head})
	case "c":
		r.hub.closeConnections(e.Account, e.Reason, closeMatch(e))
	}
}

func closeMatch(e event) func(*Conn) bool {
	switch {
	case e.Session == "":
		return func(*Conn) bool { return true }
	case e.Keep:
		return func(c *Conn) bool { return c.sessionID != e.Session }
	default:
		return func(c *Conn) bool { return c.sessionID == e.Session }
	}
}

func (r *relay) deliverUpdate(ctx context.Context, e event) {
	msg := e.Msg
	if msg == nil && r.hub.hasSubscribers(e.List) {
		up, err := r.updates.Get(ctx, e.List, e.Seq)
		if err != nil {
			// Can't deliver it: the subscribers would have a gap.
			slog.Error("read back notified update failed; closing its subscribers", "list", e.List, "seq", e.Seq, "error", err)
			r.hub.closeSubscribers(e.List)
		} else {
			msg = up.Msg
		}
	}
	if msg != nil {
		r.hub.broadcast(e.List, e.From, e.Seq, map[string]any{
			"t": "update", "list": e.List, "seq": e.Seq, "msg": msg,
		})
	}
	// Everything before it on this list was delivered first: the author
	// may ack now.
	r.mu.Lock()
	w := r.waiters[waitKey{e.List, e.Seq}]
	delete(r.waiters, waitKey{e.List, e.Seq})
	r.mu.Unlock()
	if w != nil {
		w.ok = true
		close(w.done)
	}
}

// lost stops accepting sockets, closes the ones there are and fails every
// pending ack.
func (r *relay) lost() {
	r.hub.setListening(false)
	r.hub.closeAll()
	r.mu.Lock()
	ws := r.waiters
	r.waiters = map[waitKey]*waiter{}
	r.mu.Unlock()
	for _, w := range ws {
		close(w.done)
	}
}

// expect registers interest in an update's own notification. Called
// inside the write transaction, before commit, so the notification can't
// come first.
func (r *relay) expect(list string, seq int64) *waiter {
	w := &waiter{done: make(chan struct{})}
	r.mu.Lock()
	r.waiters[waitKey{list, seq}] = w
	r.mu.Unlock()
	return w
}

func (r *relay) forget(list string, seq int64) {
	r.mu.Lock()
	delete(r.waiters, waitKey{list, seq})
	r.mu.Unlock()
}

// notifyUpdate queues the update's notification in tx: sent on commit.
func notifyUpdate(ctx context.Context, tx pgx.Tx, list string, seq int64, from string, msg []byte) error {
	e := event{Kind: "u", List: list, Seq: seq, From: from, Msg: msg}
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(payload) > maxNotifyPayload {
		e.Msg = nil
		if payload, err = json.Marshal(e); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, notifyChannel, string(payload))
	return err
}

// publish sends events outside any transaction.
func (r *relay) publish(ctx context.Context, events ...event) error {
	for _, e := range events {
		payload, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := r.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, notifyChannel, string(payload)); err != nil {
			return fmt.Errorf("notify: %w", err)
		}
	}
	return nil
}

// groupEvents splits a `group` notification so each payload stays small.
func groupEvents(group string, head int64, accounts []string) []event {
	var out []event
	for len(accounts) > 0 {
		n := min(groupChunk, len(accounts))
		out = append(out, event{Kind: "g", Group: group, Head: head, Accounts: accounts[:n]})
		accounts = accounts[n:]
	}
	return out
}
