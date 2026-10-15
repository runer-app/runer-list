package sync

import (
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"sync"

	"github.com/coder/websocket"
)

// Hub tracks locally connected WebSocket clients so live updates can be
// fanned out to other subscribers of the same list. In-process, not
// Redis — correct for the single-instance deployment (Self-hosting:
// Shape of a deployment); shared state is only needed once realtime users
// are spread across several server instances.
type Hub struct {
	mu              sync.Mutex
	subscribers     map[string]map[*Conn]bool // list id -> conns
	accountConns    map[string]map[*Conn]bool // accountID -> conns, for the per-account limit and forced close
	maxConnsPerAcct int

	// listLocks serialize "persist, then fan out" per list, so seq order,
	// commit order and delivery order are the same.
	// Kept simple: striped in-process locks, single instance only; move to
	// per-list DB sequencing if the relay ever runs on more than one node.
	listLocks [256]sync.Mutex
}

func newHub(maxConnsPerAccount int) *Hub {
	return &Hub{
		subscribers:     make(map[string]map[*Conn]bool),
		accountConns:    make(map[string]map[*Conn]bool),
		maxConnsPerAcct: maxConnsPerAccount,
	}
}

func (h *Hub) listLock(key string) *sync.Mutex {
	f := fnv.New32a()
	_, _ = f.Write([]byte(key))
	return &h.listLocks[f.Sum32()%uint32(len(h.listLocks))]
}

// tryAddConnection enforces "WebSocket connections per account" (Sync
// Protocol & API §6, default 10) and registers c for later lookup (e.g.
// forced close on removal or session end).
func (h *Hub) tryAddConnection(accountID string, c *Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.accountConns[accountID]) >= h.maxConnsPerAcct {
		return false
	}
	if h.accountConns[accountID] == nil {
		h.accountConns[accountID] = make(map[*Conn]bool)
	}
	h.accountConns[accountID][c] = true
	return true
}

func (h *Hub) removeConnection(accountID string, c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.accountConns[accountID], c)
	if len(h.accountConns[accountID]) == 0 {
		delete(h.accountConns, accountID)
	}
}

func (h *Hub) subscribe(key string, c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscribers[key] == nil {
		h.subscribers[key] = make(map[*Conn]bool)
	}
	h.subscribers[key][c] = true
}

func (h *Hub) unsubscribe(key string, c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers[key], c)
	if len(h.subscribers[key]) == 0 {
		delete(h.subscribers, key)
	}
}

func (h *Hub) unsubscribeAll(c *Conn) {
	for _, key := range c.subscribedKeys() {
		h.unsubscribe(key, c)
	}
}

// broadcast queues frame for every subscriber of key except exclude (nil
// excludes nobody). The author never receives their own live update back
// (Sync §4: "to every subscriber except the author"). It
// never blocks on a subscriber: a full queue disconnects that subscriber.
func (h *Hub) broadcast(key string, exclude *Conn, seq int64, frame map[string]any) {
	data, err := json.Marshal(frame)
	if err != nil {
		slog.Error("marshal broadcast frame failed", "error", err)
		return
	}
	h.mu.Lock()
	targets := make([]*Conn, 0, len(h.subscribers[key]))
	for c := range h.subscribers[key] {
		if c != exclude {
			targets = append(targets, c)
		}
	}
	h.mu.Unlock()

	for _, c := range targets {
		c.deliver(key, liveFrame{seq: seq, data: data})
	}
}

// sendToAccounts queues frame for every live connection of accountIDs,
// without waiting on any of them.
func (h *Hub) sendToAccounts(accountIDs []string, frame map[string]any) {
	data, err := json.Marshal(frame)
	if err != nil {
		slog.Error("marshal frame failed", "error", err)
		return
	}
	h.mu.Lock()
	var targets []*Conn
	for _, id := range accountIDs {
		for c := range h.accountConns[id] {
			targets = append(targets, c)
		}
	}
	h.mu.Unlock()

	for _, c := range targets {
		c.mu.Lock()
		c.push(data)
		c.mu.Unlock()
	}
}

// closeConnections force-closes accountID's connections that match, after
// a best-effort `bye` frame (E2EE §8.1: "closes Bob's sockets"; Sync
// §3, `bye`). One WebSocket multiplexes all of an app
// instance's subscriptions (Sync §1), so this is a
// whole-connection close. It never blocks on a peer.
func (h *Hub) closeConnections(accountID, reason string, match func(*Conn) bool) {
	h.mu.Lock()
	var targets []*Conn
	for c := range h.accountConns[accountID] {
		if match(c) {
			targets = append(targets, c)
		}
	}
	h.mu.Unlock()

	bye, _ := json.Marshal(map[string]any{"t": "bye", "reason": reason})
	for _, c := range targets {
		c.shutdown(websocket.StatusNormalClosure, reason, bye)
	}
}
