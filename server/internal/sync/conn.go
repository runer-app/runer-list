package sync

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"
)

const (
	// sendQueueLen bounds frames waiting for one connection's writer. A
	// broadcast that finds it full disconnects the connection (slow
	// consumer); the client recovers through catch-up.
	sendQueueLen = 256
	// ownQueueLen caps how much of the queue this connection's own frames
	// (catch-up, acks, released held frames) may fill. The rest is left to
	// live fan-out, so a long catch-up never makes push find it full.
	ownQueueLen = 64
	// maxHeldLive bounds live frames held back for one list while its
	// catch-up is still being sent.
	maxHeldLive  = 1024
	writeTimeout = 10 * time.Second
	byeTimeout   = time.Second
)

var errConnClosed = errors.New("connection closed")

// Conn wraps one WebSocket connection: its subscriptions, rate limits, and
// a single writer goroutine fed by a bounded queue, so no other connection's
// goroutine ever blocks on this peer's socket.
type Conn struct {
	ws         *websocket.Conn
	accountID  string
	sessionID  string
	limiter    *rate.Limiter // update/snapshot frames (Sync §6: 50/s)
	subLimiter *rate.Limiter // sub frames: each can fan out to hundreds of DB reads
	// frameLimiter is the budget for every frame, checked before the
	// session query and JSON decode that every frame otherwise costs.
	// Above the write budget, so it only bites on junk or abuse.
	frameLimiter *rate.Limiter

	out       chan []byte
	wrote     chan struct{} // signalled after each write; wakes a waiting send
	closed    chan struct{}
	closeOnce sync.Once

	mu   sync.Mutex
	subs map[string]bool // key: list id
	// held holds live frames for lists whose catch-up is in progress, so
	// they are delivered after the history, in seq order, without gaps.
	held map[string][]liveFrame
}

type liveFrame struct {
	seq  int64 // 0 for frames without a list seq
	data []byte
}

func newConn(ws *websocket.Conn, accountID, sessionID string) *Conn {
	c := &Conn{
		ws: ws, accountID: accountID, sessionID: sessionID,
		limiter:      rate.NewLimiter(50, 50),
		subLimiter:   rate.NewLimiter(5, 10),
		frameLimiter: rate.NewLimiter(100, 100),
		out:          make(chan []byte, sendQueueLen),
		wrote:        make(chan struct{}, 1),
		closed:       make(chan struct{}),
		subs:         make(map[string]bool),
		held:         make(map[string][]liveFrame),
	}
	go c.writeLoop()
	return c
}

func (c *Conn) writeLoop() {
	for {
		select {
		case data := <-c.out:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				c.shutdown(websocket.StatusPolicyViolation, "write failed", nil)
				return
			}
			select {
			case c.wrote <- struct{}{}:
			default:
			}
		case <-c.closed:
			return
		}
	}
}

// send queues a frame from this connection's own goroutine (acks, errors,
// catch-up). It waits until fewer than ownQueueLen frames are queued:
// back-pressure only stalls this connection, and a stuck peer is cut off by
// the writer's deadline. Only the read goroutine calls it.
func (c *Conn) send(ctx context.Context, frame map[string]any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return c.sendRaw(ctx, data)
}

func (c *Conn) sendRaw(ctx context.Context, data []byte) error {
	for len(c.out) >= ownQueueLen {
		select {
		case <-c.wrote:
		case <-c.closed:
			return errConnClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case c.out <- data:
		return nil
	case <-c.closed:
		return errConnClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// push queues a frame from another goroutine (fan-out). It never waits.
// Callers hold c.mu so frames for one list keep their order.
func (c *Conn) push(data []byte) {
	select {
	case c.out <- data:
	default:
		c.shutdown(websocket.StatusPolicyViolation, "slow consumer", nil)
	}
}

// deliver hands a live frame for key to this connection: held back while
// that list's catch-up is running, queued otherwise.
func (c *Conn) deliver(key string, f liveFrame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if held, catchingUp := c.held[key]; catchingUp {
		if len(held) >= maxHeldLive {
			c.shutdown(websocket.StatusPolicyViolation, "slow consumer", nil)
			return
		}
		c.held[key] = append(held, f)
		return
	}
	c.push(f.data)
}

// beginCatchUp subscribes key and starts holding its live frames.
func (c *Conn) beginCatchUp(key string) {
	c.mu.Lock()
	c.subs[key] = true
	c.held[key] = []liveFrame{}
	c.mu.Unlock()
}

// finishCatchUp releases held frames newer than head (older ones were in
// the history just sent) and switches key to direct delivery. Frames are
// released with send's back-pressure, outside c.mu so fan-out never waits;
// frames held meanwhile go out in the next round, and direct delivery
// starts only once nothing is held, keeping per-list order.
func (c *Conn) finishCatchUp(ctx context.Context, key string, head int64) error {
	for {
		c.mu.Lock()
		held := c.held[key]
		if len(held) == 0 {
			delete(c.held, key)
			c.mu.Unlock()
			return nil
		}
		c.held[key] = []liveFrame{}
		c.mu.Unlock()

		for _, f := range held {
			if f.seq == 0 || f.seq > head {
				if err := c.sendRaw(ctx, f.data); err != nil {
					return err
				}
			}
		}
	}
}

// shutdown closes the connection once. bye, if given, is written best-effort
// with a short deadline first; it can never delay or prevent the close.
// Runs the network part in the background so callers never block on a peer.
func (c *Conn) shutdown(code websocket.StatusCode, reason string, bye []byte) {
	c.closeOnce.Do(func() {
		close(c.closed)
		go func() {
			if bye != nil {
				ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
				_ = c.ws.Write(ctx, websocket.MessageText, bye)
				cancel()
			}
			_ = c.ws.Close(code, reason)
		}()
	})
}

func (c *Conn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *Conn) unsubscribe(key string) {
	c.mu.Lock()
	delete(c.subs, key)
	delete(c.held, key)
	c.mu.Unlock()
}

func (c *Conn) subscriptionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subs)
}

func (c *Conn) subscribedKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.subs))
	for k := range c.subs {
		keys = append(keys, k)
	}
	return keys
}
