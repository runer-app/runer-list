package testclient

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// SyncConn is a real WebSocket connection to /v1/sync, using the same
// authenticated http.Client (and thus session cookie) as the rest of this
// Client — exactly how a real client-core connection would be
// authenticated.
type SyncConn struct {
	ws *websocket.Conn
}

// DialSync opens the sync WebSocket.
func (c *Client) DialSync(ctx context.Context) (*SyncConn, error) {
	wsURL := strings.Replace(c.BaseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)

	ws, _, err := websocket.Dial(ctx, wsURL+"/v1/sync", &websocket.DialOptions{
		HTTPClient: c.http,
		HTTPHeader: http.Header{"Origin": []string{c.Origin}},
	})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(32 << 20) // server frames go up to 16 MiB snapshots
	return &SyncConn{ws: ws}, nil
}

func (s *SyncConn) Close() error {
	return s.ws.Close(websocket.StatusNormalClosure, "")
}

func (s *SyncConn) Send(ctx context.Context, frame map[string]any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return s.ws.Write(ctx, websocket.MessageText, data)
}

// recvTimeout bounds a read whose ctx has no deadline, so a frame the
// server never sends fails the test instead of hanging the suite.
const recvTimeout = 10 * time.Second

// Recv reads and JSON-decodes one frame.
func (s *SyncConn) Recv(ctx context.Context) (map[string]any, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, recvTimeout)
		defer cancel()
	}
	_, data, err := s.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RecvUntil reads frames until one satisfies match, or ctx is done. Other
// frames are discarded — real sync traffic is not strictly ordered
// relative to what a test cares about (e.g. a `group` frame sent at
// connect vs. the `synced` frame a test is waiting for).
func (s *SyncConn) RecvUntil(ctx context.Context, match func(map[string]any) bool) (map[string]any, error) {
	for {
		f, err := s.Recv(ctx)
		if err != nil {
			return nil, err
		}
		if match(f) {
			return f, nil
		}
	}
}

// RecvTwo reads frames until both match1 and match2 have each matched one
// (possibly the same connection interleaving two independent events, e.g.
// an ack for your own send and a live broadcast of someone else's — order
// between them is not guaranteed). Unlike calling RecvUntil twice in a
// row, this never discards a frame that satisfies the SECOND predicate
// while still waiting on the first.
func (s *SyncConn) RecvTwo(ctx context.Context, match1, match2 func(map[string]any) bool) (f1, f2 map[string]any, err error) {
	for f1 == nil || f2 == nil {
		f, err := s.Recv(ctx)
		if err != nil {
			return nil, nil, err
		}
		if f1 == nil && match1(f) {
			f1 = f
			continue
		}
		if f2 == nil && match2(f) {
			f2 = f
			continue
		}
	}
	return f1, f2, nil
}

func (s *SyncConn) Sub(ctx context.Context, list string, since int64) error {
	return s.Send(ctx, map[string]any{
		"t": "sub", "lists": []map[string]any{{"list": list, "since": since}},
	})
}

// UpdateHeader is the signed header an update/snapshot frame carries
// (E2EE §7), in canonical key order. SendUpdate and SendSnapshot set Type
// and CoversSeq. Author is the client's identity signing key — the server
// cross-checks it against the authenticated session, never trusting it
// alone.
type UpdateHeader struct {
	Type      string `json:"type"`
	List      string `json:"list"`
	Epoch     int64  `json:"epoch"`
	CoversSeq *int64 `json:"covers_seq,omitempty"`
	Author    string `json:"author"`
	LogHead   string `json:"log_head"`
}

func (c *Client) UpdateHeader(list string, epoch int64, logHead string) UpdateHeader {
	return UpdateHeader{List: list, Epoch: epoch, Author: b64(c.Identity.SignPub), LogHead: logHead}
}

// SendUpdate sends an `update` frame. ct/nonce/sig are opaque as far as
// the server is concerned — this test client does not implement the
// client-core content-encryption layer, so it sends plaintext
// test content directly, which is exactly what makes the plaintext-canary
// test meaningful: the server must never interpret or transform it.
func (s *SyncConn) SendUpdate(ctx context.Context, cid string, header UpdateHeader, ct string) error {
	header.Type, header.CoversSeq = "update", nil
	return s.sendWrite(ctx, cid, header, ct)
}

func (s *SyncConn) SendSnapshot(ctx context.Context, cid string, header UpdateHeader, coversSeq int64, ct string) error {
	header.Type, header.CoversSeq = "snapshot", &coversSeq
	return s.sendWrite(ctx, cid, header, ct)
}

// sendWrite sends the header as base64url of its exact canonical bytes —
// the bytes the author signs, relayed and stored unchanged (E2EE §7).
func (s *SyncConn) sendWrite(ctx context.Context, cid string, header UpdateHeader, ct string) error {
	headerBytes, err := canonicalJSON(header)
	if err != nil {
		return err
	}
	return s.Send(ctx, map[string]any{
		"t": header.Type, "cid": cid, "header": b64(headerBytes),
		"nonce": "bm9uY2U", "ct": ct, "sig": "c2ln",
	})
}
