package sync

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/grouplog"
	"runer-api/internal/store"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

const (
	maxUpdateFrameBytes   = 512 * 1024
	maxSnapshotFrameBytes = 16 * 1024 * 1024
	maxSubscriptions      = 500
	maxConnsPerAccount    = 10
	catchUpBatch          = 500
	// Sync §2: ping every 30 s, close with no pong for 60 s.
	pingInterval = 30 * time.Second
	pongTimeout  = 60 * time.Second
)

var errCoversBeyondHead = errors.New("covers_seq is beyond the list head")

// Handler serves the WebSocket relay: sub/unsub, update, snapshot,
// catch-up, and live fan-out (Sync).
type Handler struct {
	groups    *store.Groups
	accounts  *store.Accounts
	sessions  *store.Sessions
	updates   *store.Updates
	snapshots *store.Snapshots
	origin    string
	hub       *Hub
}

func NewHandler(groups *store.Groups, accounts *store.Accounts, sessions *store.Sessions, updates *store.Updates, snapshots *store.Snapshots, origin string) *Handler {
	return &Handler{
		groups: groups, accounts: accounts, sessions: sessions, updates: updates, snapshots: snapshots,
		origin: origin, hub: newHub(maxConnsPerAccount),
	}
}

func (h *Handler) RegisterRoutes(g *echo.Group, requireSession echo.MiddlewareFunc) {
	g.GET("/sync", h.serveWS, requireSession)
}

// byeSessionRevoked is the `bye` reason for every session-driven close
// (Sync §3): the client does not retry on it.
const byeSessionRevoked = "session-revoked"

// CloseAccountConnections implements grouplog.Notifier: force-
// closes every live connection for accountID after a group removal/leave.
func (h *Handler) CloseAccountConnections(_ context.Context, accountID, reason string) {
	h.hub.closeConnections(accountID, reason, func(*Conn) bool { return true })
}

// CloseAccountSessions closes every connection of a deleted account.
func (h *Handler) CloseAccountSessions(ctx context.Context, accountID string) {
	h.CloseAccountConnections(ctx, accountID, byeSessionRevoked)
}

// CloseSessionConnections force-closes the connections opened with one
// session (logout, device revocation). The per-frame session check is the
// authority; this just makes the cut-off prompt for idle listeners too.
func (h *Handler) CloseSessionConnections(accountID, sessionID string) {
	h.hub.closeConnections(accountID, byeSessionRevoked, func(c *Conn) bool { return c.sessionID == sessionID })
}

// CloseOtherSessionConnections force-closes every connection of accountID
// not opened with keepSessionID (rekey revokes all other sessions).
func (h *Handler) CloseOtherSessionConnections(accountID, keepSessionID string) {
	h.hub.closeConnections(accountID, byeSessionRevoked, func(c *Conn) bool { return c.sessionID != keepSessionID })
}

// NotifyGroup implements grouplog.Notifier: tells every live connection of
// accountIDs that a membership log grew or the group was deleted (Sync §3,
// `group`), so clients refetch and re-verify without polling.
func (h *Handler) NotifyGroup(_ context.Context, groupID string, headSeq int64, accountIDs []string) {
	h.hub.sendToAccounts(accountIDs, map[string]any{"t": "group", "group": groupID, "head_seq": headSeq})
}

// serveWS upgrades the connection. Origin is checked here explicitly
// (E2EE §3.4: "on... the WebSocket upgrade") rather than via
// coder/websocket's own origin verification, which assumes web-standard
// http(s) origins and doesn't understand the app's tauri:// scheme.
func (h *Handler) serveWS(c *echo.Context) error {
	if c.Request().Header.Get("Origin") != h.origin {
		return echo.NewHTTPError(http.StatusForbidden, "origin mismatch")
	}
	acc, _ := auth.AccountFromContext(c)
	sess, _ := auth.SessionFromContext(c)

	ws, err := websocket.Accept(c.Response(), c.Request(), &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return err
	}
	defer func() { _ = ws.CloseNow() }()
	// coder/websocket's own default read limit (32KiB) is well under our
	// largest documented frame (16MiB snapshots); raise it so oversized
	// frames reach OUR too-large check instead of killing the connection
	// at the protocol level first.
	ws.SetReadLimit(maxSnapshotFrameBytes + 4096)

	conn := newConn(ws, acc.ID, sess.ID)
	defer conn.shutdown(websocket.StatusNormalClosure, "", nil)
	if !h.hub.tryAddConnection(acc.ID, conn) {
		_ = ws.Close(websocket.StatusPolicyViolation, "too many connections for this account")
		return nil
	}
	defer h.hub.removeConnection(acc.ID, conn)

	ctx := c.Request().Context()
	defer h.hub.unsubscribeAll(conn)

	go keepAlive(ctx, conn, pingInterval, pongTimeout, func() bool { return h.sessionLive(ctx, conn) })

	h.sendInitialGroups(ctx, conn, acc.ID)

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return nil //nolint:nilerr // connection closed or context done; not a request error
		}
		if conn.isClosed() {
			continue // closing: drop frames until Read fails
		}
		if !conn.frameLimiter.Allow() {
			h.sendError(ctx, conn, "", "rate-limited", "")
			continue
		}
		// The upgrade-time session check isn't enough: logout, revocation,
		// rekey, expiry and account deletion must cut off open sockets too.
		if !h.sessionLive(ctx, conn) {
			endSession(conn)
			continue
		}
		h.handleFrame(ctx, conn, data)
	}
}

// sessionLive re-checks, in the database, that the connection's session is
// unexpired and unrevoked and its account not deleted. A lookup error
// counts as dead: fail closed, the client reconnects.
func (h *Handler) sessionLive(ctx context.Context, conn *Conn) bool {
	sess, err := h.sessions.GetByID(ctx, conn.sessionID)
	if err != nil || sess.AccountID != conn.accountID || sess.Expired(time.Now()) {
		return false
	}
	_, err = h.accounts.GetByID(ctx, conn.accountID)
	return err == nil
}

func endSession(conn *Conn) {
	bye, _ := json.Marshal(map[string]any{"t": "bye", "reason": byeSessionRevoked})
	conn.shutdown(websocket.StatusPolicyViolation, "session ended", bye)
}

// keepAlive pings every interval and closes the connection when a pong
// doesn't arrive within timeout, or when live() reports the session ended
// — so expiry is enforced even on a socket that only listens.
func keepAlive(ctx context.Context, conn *Conn, interval, timeout time.Duration, live func() bool) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-conn.closed:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		err := conn.ws.Ping(pctx)
		cancel()
		if err != nil {
			conn.shutdown(websocket.StatusPolicyViolation, "ping timeout", nil)
			return
		}
		if !live() {
			endSession(conn)
			return
		}
	}
}

// sendInitialGroups sends, at connect, a `group` frame per list (Sync §3),
// so a client that was offline learns of every membership change.
func (h *Handler) sendInitialGroups(ctx context.Context, conn *Conn, accountID string) {
	groups, err := h.groups.ListForAccount(ctx, accountID)
	if err != nil {
		slog.Warn("list groups for initial group frames failed", "error", err)
		return
	}
	for _, g := range groups {
		_ = conn.send(ctx, map[string]any{"t": "group", "group": g.ID, "head_seq": g.HeadSeq})
	}
}

func (h *Handler) handleFrame(ctx context.Context, conn *Conn, data []byte) {
	var f clientFrame
	if err := json.Unmarshal(data, &f); err != nil {
		h.sendError(ctx, conn, "", "bad-request", "malformed frame")
		return
	}

	switch f.T {
	case "sub":
		h.handleSub(ctx, conn, f)
	case "unsub":
		h.handleUnsub(conn, f)
	case "update":
		h.handleUpdate(ctx, conn, data, f)
	case "snapshot":
		h.handleSnapshot(ctx, conn, data, f)
	default:
		h.sendError(ctx, conn, f.Cid, "bad-request", "unknown frame type")
	}
}

func (h *Handler) sendError(ctx context.Context, conn *Conn, cid, code, detail string) {
	frame := map[string]any{"t": "error", "code": code}
	if cid != "" {
		frame["cid"] = cid
	}
	if detail != "" {
		frame["detail"] = detail
	}
	_ = conn.send(ctx, frame)
}

func (h *Handler) handleSub(ctx context.Context, conn *Conn, f clientFrame) {
	if !conn.subLimiter.Allow() {
		h.sendError(ctx, conn, f.Cid, "rate-limited", "")
		return
	}
	if conn.subscriptionCount()+len(f.Lists) > maxSubscriptions {
		h.sendError(ctx, conn, f.Cid, "too-large", "too many subscribed lists")
		return
	}
	for _, l := range f.Lists {
		h.subscribeOne(ctx, conn, l)
	}
}

func (h *Handler) subscribeOne(ctx context.Context, conn *Conn, l subList) {
	if _, _, err := h.groups.Access(ctx, l.List, conn.accountID); err != nil {
		h.sendError(ctx, conn, "", "forbidden", "not a member of "+l.List)
		return
	}

	// Register for live frames BEFORE reading history, holding them back
	// until the history is out: every update is then either in the history
	// or held, and held ones already sent are dropped by seq.
	key := l.List
	conn.beginCatchUp(key)
	h.hub.subscribe(key, conn)

	head, err := h.catchUp(ctx, conn, l)
	if err != nil {
		h.hub.unsubscribe(key, conn)
		conn.unsubscribe(key)
		if !errors.Is(err, errConnClosed) {
			slog.Warn("catch-up failed", "error", err)
			h.sendError(ctx, conn, "", "bad-request", "catch-up failed for "+l.List+"; resubscribe")
		}
		return
	}
	_ = conn.send(ctx, map[string]any{"t": "synced", "list": l.List, "head": head})
	_ = conn.finishCatchUp(ctx, key, head) // fails only once the conn is closing
}

// catchUp sends the best snapshot and the update tail after it, in pages
// of catchUpBatch (Sync §4). Returns the last seq sent (or
// the starting point if nothing newer exists).
func (h *Handler) catchUp(ctx context.Context, conn *Conn, l subList) (int64, error) {
	head := l.Since
	snap, err := h.snapshots.Best(ctx, l.List, l.Since)
	switch {
	case err == nil:
		if err := conn.send(ctx, map[string]any{
			"t": "snapshot", "list": l.List,
			"epoch": snap.Epoch, "covers_seq": snap.CoversSeq, "msg": json.RawMessage(snap.Msg),
		}); err != nil {
			return 0, err
		}
		head = snap.CoversSeq
	case !errors.Is(err, store.ErrNotFound):
		return 0, err
	}

	for {
		batch, err := h.updates.ListSince(ctx, l.List, head, catchUpBatch)
		if err != nil {
			return 0, err
		}
		for _, u := range batch {
			if err := conn.send(ctx, map[string]any{"t": "update", "list": l.List, "seq": u.Seq, "msg": json.RawMessage(u.Msg)}); err != nil {
				return 0, err
			}
			head = u.Seq
		}
		if len(batch) < catchUpBatch {
			return head, nil
		}
	}
}

func (h *Handler) handleUnsub(conn *Conn, f clientFrame) {
	for _, l := range f.Lists {
		h.hub.unsubscribe(l.List, conn)
		conn.unsubscribe(l.List)
	}
}

// parseWrite parses an update/snapshot frame's header and checks
// header.author is the session's identity (E2EE §7). Membership and epoch
// are checked inside the write's own transaction
// (store.Groups.AuthorizedWrite). On failure it has already sent the error
// frame and returns ok=false.
func (h *Handler) parseWrite(ctx context.Context, conn *Conn, f clientFrame) (hdr updateHeader, msgBytes []byte, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(f.Header)
	if err == nil {
		err = json.Unmarshal(raw, &hdr)
	}
	if err == nil {
		err = requireCanonicalHeader(raw, hdr)
	}
	if err != nil {
		h.sendError(ctx, conn, f.Cid, "bad-request", "malformed header")
		return hdr, nil, false
	}
	if hdr.Type != f.T || (hdr.Type == "snapshot") != (hdr.CoversSeq != nil) {
		h.sendError(ctx, conn, f.Cid, "bad-request", "header type or covers_seq does not match the frame")
		return hdr, nil, false
	}

	acc, err := h.accounts.GetByID(ctx, conn.accountID)
	if err != nil || hdr.Author != base64.RawURLEncoding.EncodeToString(acc.IdentitySignPub) {
		h.sendError(ctx, conn, f.Cid, "forbidden", "header.author does not match the authenticated session")
		return hdr, nil, false
	}

	msgBytes, err = json.Marshal(relayedMsg{Header: f.Header, Nonce: f.Nonce, CT: f.CT, Sig: f.Sig})
	if err != nil {
		h.sendError(ctx, conn, f.Cid, "bad-request", "")
		return hdr, nil, false
	}
	return hdr, msgBytes, true
}

// requireCanonicalHeader rejects a header that isn't exactly the canonical
// encoding of what it decodes to (same rule as log entries, E2EE §4.1), so
// server and client can't read different routing fields from one
// signature.
func requireCanonicalHeader(raw []byte, hdr updateHeader) error {
	canonical, err := grouplog.CanonicalJSON(hdr)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return errors.New("header not in canonical form")
	}
	for _, n := range []int64{hdr.Epoch, derefOr(hdr.CoversSeq, 0)} {
		if n < 0 || n > grouplog.MaxSafeInt {
			return errors.New("integer out of range")
		}
	}
	return nil
}

func derefOr(p *int64, fallback int64) int64 {
	if p == nil {
		return fallback
	}
	return *p
}

// sendWriteError maps an AuthorizedWrite failure to its frame code.
func (h *Handler) sendWriteError(ctx context.Context, conn *Conn, cid string, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden):
		h.sendError(ctx, conn, cid, "forbidden", "")
	case errors.Is(err, store.ErrStaleEpoch):
		h.sendError(ctx, conn, cid, "stale-epoch", "")
	case errors.Is(err, errCoversBeyondHead):
		h.sendError(ctx, conn, cid, "bad-request", err.Error())
	default:
		slog.Error("list write failed", "error", err)
		h.sendError(ctx, conn, cid, "bad-request", "")
	}
}

// handleUpdate persists one encrypted Yjs update and fans it out to other
// subscribers (Sync §3-4; E2EE §7 for the server-side
// checks). The server never inspects header/nonce/ct/sig content — it
// only parses header's plaintext routing fields.
func (h *Handler) handleUpdate(ctx context.Context, conn *Conn, raw []byte, f clientFrame) {
	if len(raw) > maxUpdateFrameBytes {
		h.sendError(ctx, conn, f.Cid, "too-large", "update exceeds size limit")
		return
	}
	if !conn.limiter.Allow() {
		h.sendError(ctx, conn, f.Cid, "rate-limited", "")
		return
	}

	hdr, msgBytes, ok := h.parseWrite(ctx, conn, f)
	if !ok {
		return
	}

	// Persist and fan out under the list lock: seq order is commit order is
	// delivery order.
	lock := h.hub.listLock(hdr.List)
	lock.Lock()
	var up store.Update
	err := h.groups.AuthorizedWrite(ctx, hdr.List, conn.accountID, hdr.Epoch, func(tx pgx.Tx) error {
		var err error
		up, err = h.updates.Append(ctx, tx, hdr.List, hdr.Epoch, conn.accountID, msgBytes)
		return err
	})
	if err == nil {
		h.hub.broadcast(hdr.List, conn, up.Seq, map[string]any{
			"t": "update", "list": hdr.List, "seq": up.Seq, "msg": json.RawMessage(msgBytes),
		})
	}
	lock.Unlock()
	if err != nil {
		h.sendWriteError(ctx, conn, f.Cid, err)
		return
	}

	if f.Cid != "" {
		_ = conn.send(ctx, map[string]any{"t": "ack", "cid": f.Cid, "seq": up.Seq})
	}
}

func (h *Handler) handleSnapshot(ctx context.Context, conn *Conn, raw []byte, f clientFrame) {
	if len(raw) > maxSnapshotFrameBytes {
		h.sendError(ctx, conn, f.Cid, "too-large", "snapshot exceeds size limit")
		return
	}
	// Shares the per-connection budget with updates: a snapshot is a
	// bigger write, never a free one.
	if !conn.limiter.Allow() {
		h.sendError(ctx, conn, f.Cid, "rate-limited", "")
		return
	}

	hdr, msgBytes, ok := h.parseWrite(ctx, conn, f)
	if !ok {
		return
	}

	err := h.groups.AuthorizedWrite(ctx, hdr.List, conn.accountID, hdr.Epoch, func(tx pgx.Tx) error {
		// covers_seq beyond the list's head would make catch-up skip every
		// future update, and GREATEST in Upsert would keep it forever.
		head, err := h.updates.HeadSeq(ctx, tx, hdr.List)
		if err != nil {
			return err
		}
		if *hdr.CoversSeq > head {
			return errCoversBeyondHead
		}
		return h.snapshots.Upsert(ctx, tx, hdr.List, hdr.Epoch, *hdr.CoversSeq, msgBytes)
	})
	if err != nil {
		h.sendWriteError(ctx, conn, f.Cid, err)
		return
	}

	if f.Cid != "" {
		_ = conn.send(ctx, map[string]any{"t": "ack", "cid": f.Cid, "seq": *hdr.CoversSeq})
	}
}
