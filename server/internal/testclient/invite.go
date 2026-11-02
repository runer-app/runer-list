package testclient

import (
	"context"
	"encoding/base64"
	"net/http"
	"time"
)

type InviteView struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (c *Client) CreateInvite(ctx context.Context, groupID, inviteID string, pkg []byte, expiresAt time.Time) (InviteView, error) {
	var out InviteView
	err := c.do(ctx, http.MethodPost, "/v1/groups/"+groupID+"/invites", map[string]any{
		"invite_id": inviteID, "package": b64(pkg), "expires_at": expiresAt,
	}, &out)
	return out, err
}

// CreateLinkInvite stores a link invite's package: invitePub is the key
// the list's open `invite` entry names (E2EE §6).
func (c *Client) CreateLinkInvite(ctx context.Context, groupID, inviteID string, pkg []byte, expiresAt time.Time, invitePub []byte) (InviteView, error) {
	var out InviteView
	err := c.do(ctx, http.MethodPost, "/v1/groups/"+groupID+"/invites", map[string]any{
		"invite_id": inviteID, "package": b64(pkg), "expires_at": expiresAt, "invite_pub": b64(invitePub),
	}, &out)
	return out, err
}

// InviteLog reads the invite's list log before joining.
func (c *Client) InviteLog(ctx context.Context, inviteID string) ([]LogEntryView, error) {
	var out []LogEntryView
	err := c.do(ctx, http.MethodGet, "/v1/invites/"+inviteID+"/log", nil, &out)
	return out, err
}

// AcceptInvite signs and appends the client's own `join` entry.
func (c *Client) AcceptInvite(ctx context.Context, inviteID string, entry GroupEntry) (GroupView, error) {
	raw, sig, err := c.SignGroupEntry(entry)
	if err != nil {
		return GroupView{}, err
	}
	var out GroupView
	err = c.do(ctx, http.MethodPost, "/v1/invites/"+inviteID+"/accept", map[string]string{"entry": b64(raw), "sig": b64(sig)}, &out)
	return out, err
}

func (c *Client) GetInvitePackage(ctx context.Context, inviteID string) ([]byte, error) {
	var out struct {
		Package string `json:"package"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/invites/"+inviteID, nil, &out); err != nil {
		return nil, err
	}
	return base64.RawURLEncoding.DecodeString(out.Package)
}

type JoinRequestResult struct {
	ID string `json:"id"`
}

func (c *Client) JoinInvite(ctx context.Context, inviteID string, encDisplayName, mac []byte) (JoinRequestResult, error) {
	var out JoinRequestResult
	err := c.do(ctx, http.MethodPost, "/v1/invites/"+inviteID+"/join", map[string]string{
		"enc_display_name": b64(encDisplayName), "mac": b64(mac),
	}, &out)
	return out, err
}

type JoinRequestView struct {
	ID               string `json:"id"`
	InviteID         string `json:"invite_id"`
	AccountID        string `json:"account_id"`
	IdentitySignPub  string `json:"identity_sign_pub"`
	IdentityAgreePub string `json:"identity_agree_pub"`
	EncDisplayName   string `json:"enc_display_name"`
	MAC              string `json:"mac"`
}

func (c *Client) ListJoinRequests(ctx context.Context, groupID string) ([]JoinRequestView, error) {
	var out []JoinRequestView
	err := c.do(ctx, http.MethodGet, "/v1/groups/"+groupID+"/join-requests", nil, &out)
	return out, err
}

func (c *Client) RevokeInvite(ctx context.Context, inviteID string) error {
	return c.do(ctx, http.MethodDelete, "/v1/invites/"+inviteID, nil, nil)
}
