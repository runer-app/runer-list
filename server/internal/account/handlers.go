// Package account implements GET /account, PUT /account/settings,
// GET /account/bundle, POST /account/rekey and DELETE /account.
package account

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"

	"runer-api/internal/auth"
	"runer-api/internal/httpx"
	"runer-api/internal/store"

	"github.com/labstack/echo/v5"
)

type Handler struct {
	accounts   *store.Accounts
	sessions   *store.Sessions
	serverURL  string // canonical API URL covered by rekey signatures
	challenges *rekeyChallenges
	conns      Conns
}

// Conns is what the handler needs of live sync connections: close them
// when sessions end, and tell the members of a list that it changed.
// Implemented by the sync handler.
type Conns interface {
	auth.ConnCloser
	NotifyGroup(ctx context.Context, groupID string, headSeq int64, accountIDs []string)
}

func NewHandler(accounts *store.Accounts, sessions *store.Sessions, serverURL string, conns Conns) *Handler {
	return &Handler{accounts: accounts, sessions: sessions, serverURL: serverURL, challenges: newRekeyChallenges(), conns: conns}
}

func (h *Handler) RegisterRoutes(g *echo.Group, requireSession echo.MiddlewareFunc) {
	g.GET("/account", h.getAccount, requireSession)
	g.PUT("/account/settings", h.setSettings, requireSession)
	g.GET("/account/bundle", h.getBundle, requireSession)
	g.POST("/account/rekey/challenge", h.rekeyChallenge, requireSession)
	g.POST("/account/rekey", h.rekey, requireSession)
	g.DELETE("/account", h.deleteAccount, requireSession)
}

type accountView struct {
	ID               string  `json:"id"`
	IdentitySignPub  string  `json:"identity_sign_pub"`
	IdentityAgreePub string  `json:"identity_agree_pub"`
	BundleGen        int     `json:"bundle_gen"`
	CreatedAt        string  `json:"created_at"`
	LastSeenAt       string  `json:"last_seen_at"`
	InboxID          *string `json:"inbox_id"`
	SettingsSealed   *string `json:"settings_sealed"` // null until first set
	SettingsGen      int     `json:"settings_gen"`
}

func (h *Handler) getAccount(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	var settingsSealed *string
	if acc.SettingsSealed != nil {
		s := base64.RawURLEncoding.EncodeToString(acc.SettingsSealed)
		settingsSealed = &s
	}
	return httpx.JSONData(c, http.StatusOK, accountView{
		ID:               acc.ID,
		IdentitySignPub:  base64.RawURLEncoding.EncodeToString(acc.IdentitySignPub),
		IdentityAgreePub: base64.RawURLEncoding.EncodeToString(acc.IdentityAgreePub),
		BundleGen:        acc.BundleGen,
		CreatedAt:        acc.CreatedAt.Format(http.TimeFormat),
		LastSeenAt:       acc.LastSeenAt.Format(http.TimeFormat),
		InboxID:          acc.InboxID,
		SettingsSealed:   settingsSealed,
		SettingsGen:      acc.SettingsGen,
	})
}

// Bounds of sealed account settings: nonce(12) ‖ at least 1 byte ‖ tag(16),
// up to 16 KiB.
const (
	minSettingsSealed = 12 + 1 + 16
	maxSettingsSealed = 16 << 10
)

type settingsRequest struct {
	SettingsSealed string `json:"settings_sealed"`
	ExpectedGen    *int   `json:"settings_gen" validate:"required"` // a pointer: the first write sends 0
}

// setSettings replaces the account's sealed settings. The server never opens
// them, so only their encoding and size are checked, and a write against a
// stale settings_gen is refused: the client refetches, reapplies and retries.
func (h *Handler) setSettings(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	var req settingsRequest
	if err := httpx.BindAndValidate(c, &req); err != nil {
		return err
	}
	settingsSealed, err := base64.RawURLEncoding.DecodeString(req.SettingsSealed)
	if err != nil || len(settingsSealed) < minSettingsSealed || len(settingsSealed) > maxSettingsSealed {
		return echo.NewHTTPError(http.StatusBadRequest, "missing or invalid settings_sealed")
	}
	gen, err := h.accounts.SetSettings(c.Request().Context(), acc.ID, settingsSealed, *req.ExpectedGen)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return echo.NewHTTPError(http.StatusConflict, "stale settings_gen")
		}
		return err
	}
	return httpx.JSONData(c, http.StatusOK, map[string]any{"settings_gen": gen})
}

func (h *Handler) getBundle(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	return httpx.JSONData(c, http.StatusOK, map[string]any{
		"identity_bundle": base64.RawURLEncoding.EncodeToString(acc.IdentityBundle),
		"bundle_gen":      acc.BundleGen,
	})
}

// rekeyChallenge issues a nonce the caller signs with their CURRENT auth key
// to authorize a rekey (E2EE §9.2: a session plus the old key's signature). It reuses the
// login challenge/nonce mechanism rather than inventing a new one.
func (h *Handler) rekeyChallenge(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	nonce, err := h.challenges.issue(acc.ID)
	if err != nil {
		return err
	}
	return httpx.JSONData(c, http.StatusOK, map[string]any{
		"nonce": base64.RawURLEncoding.EncodeToString(nonce),
	})
}

type rekeyRequest struct {
	Nonce       string `json:"nonce" validate:"required"`
	Sig         string `json:"sig" validate:"required"`
	NewAuthPub  string `json:"new_auth_pub" validate:"required"`
	NewBundle   string `json:"new_bundle" validate:"required"`
	ExpectedGen int    `json:"bundle_gen" validate:"required"`
}

func (h *Handler) rekey(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	sess, _ := auth.SessionFromContext(c)

	var req rekeyRequest
	if err := httpx.BindAndValidate(c, &req); err != nil {
		return err
	}

	nonce, err := httpx.DecodeKey(req.Nonce, 32)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid nonce")
	}
	sig, err := base64.RawURLEncoding.DecodeString(req.Sig)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sig")
	}
	newAuthPub, err := httpx.DecodeEd25519Key(req.NewAuthPub)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid new_auth_pub")
	}
	newBundle, err := base64.RawURLEncoding.DecodeString(req.NewBundle)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid new_bundle")
	}

	if !h.challenges.consume(acc.ID, nonce) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid or expired rekey challenge")
	}
	// Signed with the CURRENT (old) auth key, proving possession before the swap.
	if !ed25519.Verify(ed25519.PublicKey(acc.AuthPub), auth.RekeyMessage(h.serverURL, nonce), sig) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid signature")
	}

	updated, err := h.accounts.Rekey(c.Request().Context(), acc.ID, sess.ID, newAuthPub, newBundle, req.ExpectedGen)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return echo.NewHTTPError(http.StatusConflict, "stale bundle_gen")
		}
		return err
	}
	h.conns.CloseOtherSessionConnections(acc.ID, sess.ID)

	return httpx.JSONData(c, http.StatusOK, map[string]any{
		"bundle_gen": updated.BundleGen,
	})
}

// deleteAccount schedules deletion (Self-hosting: Data retention): the account
// stops resolving from here on, every session is revoked and every open
// socket closed immediately.
// The retention job purges what it safely can after the window.
func (h *Handler) deleteAccount(c *echo.Context) error {
	acc, _ := auth.AccountFromContext(c)
	ctx := c.Request().Context()

	deleted, err := h.accounts.ScheduleDeletion(ctx, acc.ID)
	if err != nil {
		return err
	}
	h.conns.CloseAccountSessions(ctx, acc.ID)
	// The lists the account owned are gone for their other members too
	// (Sync §3, `group`).
	for _, l := range deleted {
		h.conns.NotifyGroup(ctx, l.ID, l.HeadSeq, l.Members)
	}
	http.SetCookie(c.Response(), &http.Cookie{
		Name: "session", Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	return httpx.JSONData(c, http.StatusOK, map[string]any{"scheduled": true})
}
