package testclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"time"
)

// GroupEntry is the canonical entry encoding in
// protocol/docs/e2ee-protocol.md §4.1: these keys in this
// order, optional ones omitted when empty, ts in Unix milliseconds. It is
// intentionally a separate, independent definition from grouplog's
// (unexported) wireEntry — a real client-core would have its own
// independent entry-building code too; this is the client side of the
// protocol, not a reflection of the server's internals.
type GroupEntry struct {
	V            int             `json:"v"`
	Group        string          `json:"group,omitempty"`
	Seq          int64           `json:"seq"`
	Prev         string          `json:"prev,omitempty"`
	Type         string          `json:"type"`
	Author       string          `json:"author"`
	AuthorAgree  string          `json:"author_agree,omitempty"`
	Subject      string          `json:"subject,omitempty"`
	SubjectAgree string          `json:"subject_agree,omitempty"`
	Epoch        int64           `json:"epoch"`
	Envelopes    []GroupEnvelope `json:"envelopes,omitempty"`
	KeyChain     string          `json:"key_chain,omitempty"`
	Invite       string          `json:"invite,omitempty"`
	Expires      int64           `json:"expires,omitempty"`
	InviteProof  string          `json:"invite_proof,omitempty"`
	Ts           int64           `json:"ts"`
}

type GroupEnvelope struct {
	Recipient      string `json:"recipient"`
	HPKECiphertext string `json:"hpke_ciphertext"`
}

// HashEntry returns SHA-256(raw), base64url-encoded — the `prev` a
// following entry must cite, and (for a genesis entry) the group id.
func HashEntry(raw []byte) string {
	h := sha256.Sum256(raw)
	return b64(h[:])
}

// SignGroupEntry marshals and signs a group log entry with the IDENTITY
// signing key (E2EE §4.1: "signature = Ed25519(author, entry_bytes)",
// where author is the identity_sign_pub — not the login auth key).
func (c *Client) SignGroupEntry(entry GroupEntry) (raw, sig []byte, err error) {
	raw, err = canonicalJSON(entry)
	if err != nil {
		return nil, nil, err
	}
	return raw, ed25519.Sign(c.Identity.SignPriv, raw), nil
}

// InboxGenesis is the signed create entry of id's Inbox, sent at sign-up.
// Its envelope seals K_0 to id (E2EE §5).
func InboxGenesis(id *Identity) (raw, sig []byte, err error) {
	raw, err = canonicalJSON(GroupEntry{
		V: 1, Type: "create", Author: b64(id.SignPub), AuthorAgree: b64(id.AgreePub),
		Envelopes: []GroupEnvelope{{Recipient: b64(id.SignPub), HPKECiphertext: "azA"}},
		Ts:        time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, nil, err
	}
	return raw, ed25519.Sign(id.SignPriv, raw), nil
}

// canonicalJSON is compact JSON without HTML escaping: the canonical
// encoding for signed entries and headers (E2EE §4.1).
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// AgreePub is the client's identity encryption key, base64url, as signed
// into create (author_agree) and add (subject_agree) entries.
func (c *Client) AgreePub() string { return b64(c.Identity.AgreePub) }

type GroupView struct {
	ID       string `json:"id"`
	HeadSeq  int64  `json:"head_seq"`
	HeadHash string `json:"head_hash"`
	Epoch    int64  `json:"epoch"`
}

// CreateGroup fills in the genesis field only this client knows — its own
// encryption key — then signs and creates the list.
func (c *Client) CreateGroup(ctx context.Context, entry GroupEntry) (GroupView, error) {
	if entry.AuthorAgree == "" {
		entry.AuthorAgree = c.AgreePub()
	}
	raw, sig, err := c.SignGroupEntry(entry)
	if err != nil {
		return GroupView{}, err
	}
	return c.CreateGroupRaw(ctx, raw, sig)
}

func (c *Client) CreateGroupRaw(ctx context.Context, raw, sig []byte) (GroupView, error) {
	var out GroupView
	err := c.do(ctx, http.MethodPost, "/v1/groups", map[string]string{"entry": b64(raw), "sig": b64(sig)}, &out)
	return out, err
}

// AppendLog signs entry and appends it to an existing group's log.
func (c *Client) AppendLog(ctx context.Context, groupID string, entry GroupEntry) (GroupView, error) {
	raw, sig, err := c.SignGroupEntry(entry)
	if err != nil {
		return GroupView{}, err
	}
	return c.AppendLogRaw(ctx, groupID, raw, sig)
}

func (c *Client) AppendLogRaw(ctx context.Context, groupID string, raw, sig []byte) (GroupView, error) {
	var out GroupView
	err := c.do(ctx, http.MethodPost, "/v1/groups/"+groupID+"/log", map[string]string{
		"entry": b64(raw), "sig": b64(sig),
	}, &out)
	return out, err
}

func (c *Client) ListGroups(ctx context.Context) ([]GroupView, error) {
	var out []GroupView
	err := c.do(ctx, http.MethodGet, "/v1/groups", nil, &out)
	return out, err
}

type LogEntryView struct {
	Seq   int64  `json:"seq"`
	Entry string `json:"entry"`
	Sig   string `json:"sig"`
	Hash  string `json:"hash"`
}

func (c *Client) GetLog(ctx context.Context, groupID string) ([]LogEntryView, error) {
	var out []LogEntryView
	err := c.do(ctx, http.MethodGet, "/v1/groups/"+groupID+"/log", nil, &out)
	return out, err
}

type GroupMemberView struct {
	AccountID       string `json:"account_id"`
	IdentitySignPub string `json:"identity_sign_pub"`
	Role            string `json:"role"`
}

func (c *Client) ListGroupMembers(ctx context.Context, groupID string) ([]GroupMemberView, error) {
	var out []GroupMemberView
	err := c.do(ctx, http.MethodGet, "/v1/groups/"+groupID+"/members", nil, &out)
	return out, err
}

func (c *Client) DeleteGroup(ctx context.Context, groupID string) error {
	return c.do(ctx, http.MethodDelete, "/v1/groups/"+groupID, nil, nil)
}
