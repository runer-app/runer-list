package testclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"

	"runer-api/internal/auth"
	"runer-api/internal/limits"
)

// Client drives a real server over HTTP exactly as a real client would:
// no direct DB or in-process shortcuts.
type Client struct {
	BaseURL  string
	Origin   string
	Identity *Identity
	http     *http.Client

	AccountID string
	BundleGen int
	InboxID   string

	// powBits is the signup difficulty the last challenge advertised.
	powBits int

	// ip is sent as X-Forwarded-For (the server trusts it from loopback,
	// as from its proxy), so each Client is its own user to per-IP limits
	// even when many share one test server.
	ip string
}

// New creates a client with its own cookie jar (one session per Client,
// like one app instance).
func New(baseURL, origin string, id *Identity) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL:  baseURL,
		Origin:   origin,
		Identity: id,
		http:     &http.Client{Jar: jar},
		ip:       fmt.Sprintf("198.18.%d.%d", rand.IntN(256), rand.IntN(256)),
	}, nil
}

// APIError is returned for any non-2xx response.
type APIError struct {
	Status int
	Code   string
	Msg    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api error: status=%d code=%s msg=%s", e.Status, e.Code, e.Msg)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Origin != "" {
		req.Header.Set("Origin", c.Origin)
	}
	req.Header.Set("X-Forwarded-For", c.ip)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBody, &envelope)
		return &APIError{Status: resp.StatusCode, Code: envelope.Error.Code, Msg: envelope.Error.Message}
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decode envelope: %w", err)
	}
	return json.Unmarshal(envelope.Data, out)
}

// SetIP makes the client appear to come from ip (sent as X-Forwarded-For),
// e.g. to put several clients behind one office address.
func (c *Client) SetIP(ip string) { c.ip = ip }

// PoWBits is the signup difficulty the last challenge advertised.
func (c *Client) PoWBits() int { return c.powBits }

func (c *Client) challenge(ctx context.Context, authPub ed25519.PublicKey) ([]byte, error) {
	var out struct {
		Nonce   string `json:"nonce"`
		PoWBits int    `json:"pow_bits"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/auth/challenge", map[string]string{
		"auth_pub": b64(authPub),
	}, &out); err != nil {
		return nil, err
	}
	c.powBits = out.PoWBits
	nonce, err := base64.RawURLEncoding.DecodeString(out.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	return nonce, nil
}

// Challenge is the exported form of challenge, for tests that need to drive
// the challenge/login round trip manually (replay, expiry, malformed
// frames) rather than through Login/Signup.
func (c *Client) Challenge(ctx context.Context) ([]byte, error) {
	return c.challenge(ctx, c.Identity.AuthPub)
}

// SignLogin signs nonce with the identity's current auth key, exactly as
// Login does internally.
func (c *Client) SignLogin(nonce []byte) []byte {
	return ed25519.Sign(c.Identity.AuthPriv, auth.LoginMessage(c.BaseURL, nonce))
}

// RawLogin posts a bare login frame (no identity/signup fields), for tests
// exercising the server's handling of a specific nonce/sig pair directly —
// e.g. replaying an already-consumed nonce.
func (c *Client) RawLogin(ctx context.Context, nonce, sig []byte) error {
	var out loginResponse
	err := c.do(ctx, http.MethodPost, "/v1/auth/login", map[string]string{
		"auth_pub": b64(c.Identity.AuthPub),
		"nonce":    b64(nonce),
		"sig":      b64(sig),
	}, &out)
	if err == nil {
		c.setLogin(out)
	}
	return err
}

type loginResponse struct {
	AccountID string `json:"account_id"`
	BundleGen int    `json:"bundle_gen"`
	InboxID   string `json:"inbox_id"`
}

func (c *Client) setLogin(out loginResponse) {
	c.AccountID, c.BundleGen, c.InboxID = out.AccountID, out.BundleGen, out.InboxID
}

// findProofOfWork brute-forces a pow_nonce satisfying the difficulty the
// server advertised in its challenge, exactly as a real client would.
func findProofOfWork(authPub, nonce []byte, bits int) []byte {
	pow := make([]byte, 8)
	for i := uint64(0); ; i++ {
		pow[0] = byte(i)
		pow[1] = byte(i >> 8)
		pow[2] = byte(i >> 16)
		pow[3] = byte(i >> 24)
		pow[4] = byte(i >> 32)
		pow[5] = byte(i >> 40)
		pow[6] = byte(i >> 48)
		pow[7] = byte(i >> 56)
		if limits.VerifyProofOfWork(authPub, nonce, pow, bits) {
			return append([]byte(nil), pow...)
		}
	}
}

// Signup performs the combined signup+login call (E2EE §3.4): challenge,
// sign, and this time include identity fields, an opaque bundle blob, and
// proof-of-work, plus the Inbox genesis.
func (c *Client) Signup(ctx context.Context, bundle []byte) error {
	raw, sig, err := InboxGenesis(c.Identity)
	if err != nil {
		return err
	}
	return c.SignupWithInbox(ctx, bundle, raw, sig)
}

// SignupWithInbox is Signup with a caller-chosen Inbox genesis; nil sends
// none.
func (c *Client) SignupWithInbox(ctx context.Context, bundle, inboxRaw, inboxSig []byte) error {
	nonce, err := c.challenge(ctx, c.Identity.AuthPub)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(c.Identity.AuthPriv, auth.LoginMessage(c.BaseURL, nonce))
	pow := findProofOfWork(c.Identity.AuthPub, nonce, c.powBits)

	body := map[string]string{
		"auth_pub":           b64(c.Identity.AuthPub),
		"nonce":              b64(nonce),
		"sig":                b64(sig),
		"identity_sign_pub":  b64(c.Identity.SignPub),
		"identity_agree_pub": b64(c.Identity.AgreePub),
		"identity_bundle":    b64(bundle),
		"identity_sig":       b64(ed25519.Sign(c.Identity.SignPriv, auth.IdentityMessage(c.BaseURL, nonce))),
		"pow_nonce":          b64(pow),
		"inbox_entry":        b64(inboxRaw),
		"inbox_sig":          b64(inboxSig),
	}

	var out loginResponse
	if err := c.do(ctx, http.MethodPost, "/v1/auth/login", body, &out); err != nil {
		return err
	}
	c.setLogin(out)
	return nil
}

// Login performs a plain login: no identity fields, no proof-of-work.
func (c *Client) Login(ctx context.Context) error {
	nonce, err := c.challenge(ctx, c.Identity.AuthPub)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(c.Identity.AuthPriv, auth.LoginMessage(c.BaseURL, nonce))

	var out loginResponse
	if err := c.do(ctx, http.MethodPost, "/v1/auth/login", map[string]string{
		"auth_pub": b64(c.Identity.AuthPub),
		"nonce":    b64(nonce),
		"sig":      b64(sig),
	}, &out); err != nil {
		return err
	}
	c.setLogin(out)
	return nil
}

func (c *Client) Logout(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/auth/logout", nil, nil)
}

type AccountView struct {
	ID               string  `json:"id"`
	IdentitySignPub  string  `json:"identity_sign_pub"`
	IdentityAgreePub string  `json:"identity_agree_pub"`
	BundleGen        int     `json:"bundle_gen"`
	InboxID          string  `json:"inbox_id"`
	SettingsSealed   *string `json:"settings_sealed"`
	SettingsGen      int     `json:"settings_gen"`
}

func (c *Client) GetAccount(ctx context.Context) (AccountView, error) {
	var out AccountView
	err := c.do(ctx, http.MethodGet, "/v1/account", nil, &out)
	return out, err
}

// Do sends a request the typed methods don't cover and decodes the
// response's data into out, e.g. to look at raw fields or a removed route.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	return c.do(ctx, method, path, body, out)
}

// SetSettings replaces the account's sealed settings against expectedGen and
// returns the new settings_gen; settingsSealed is sent as is.
func (c *Client) SetSettings(ctx context.Context, settingsSealed string, expectedGen int) (int, error) {
	var out struct {
		SettingsGen int `json:"settings_gen"`
	}
	err := c.do(ctx, http.MethodPut, "/v1/account/settings", map[string]any{
		"settings_sealed": settingsSealed,
		"settings_gen":    expectedGen,
	}, &out)
	return out.SettingsGen, err
}

// GetBundle returns the raw bundle bytes and its generation.
func (c *Client) GetBundle(ctx context.Context) ([]byte, int, error) {
	var out struct {
		IdentityBundle string `json:"identity_bundle"`
		BundleGen      int    `json:"bundle_gen"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/account/bundle", nil, &out); err != nil {
		return nil, 0, err
	}
	bundle, err := base64.RawURLEncoding.DecodeString(out.IdentityBundle)
	if err != nil {
		return nil, 0, err
	}
	return bundle, out.BundleGen, nil
}

// Rekey changes the auth key pair (E2EE §9.2), keeping identity unchanged.
// newBundle is the (opaque) bundle re-encrypted under the new wrap key.
func (c *Client) Rekey(ctx context.Context, newBundle []byte) error {
	var chal struct {
		Nonce string `json:"nonce"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/account/rekey/challenge", nil, &chal); err != nil {
		return err
	}
	nonce, err := base64.RawURLEncoding.DecodeString(chal.Nonce)
	if err != nil {
		return err
	}

	// Signed with the OLD auth key, proving possession before the swap.
	sig := ed25519.Sign(c.Identity.AuthPriv, auth.RekeyMessage(c.BaseURL, nonce))

	newPub, newPriv, err := c.Identity.Rekey()
	if err != nil {
		return err
	}

	var out struct {
		BundleGen int `json:"bundle_gen"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/account/rekey", map[string]any{
		"nonce":        b64(nonce),
		"sig":          b64(sig),
		"new_auth_pub": b64(newPub),
		"new_bundle":   b64(newBundle),
		"bundle_gen":   c.BundleGen,
	}, &out); err != nil {
		return err
	}

	c.Identity.AuthPub = newPub
	c.Identity.AuthPriv = newPriv
	c.BundleGen = out.BundleGen
	return nil
}

func (c *Client) DeleteAccount(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/v1/account", nil, nil)
}

type SessionView struct {
	ID      string `json:"id"`
	Current bool   `json:"current"`
}

func (c *Client) ListSessions(ctx context.Context) ([]SessionView, error) {
	var out []SessionView
	err := c.do(ctx, http.MethodGet, "/v1/sessions", nil, &out)
	return out, err
}

func (c *Client) RevokeSession(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/sessions/"+id, nil, nil)
}
