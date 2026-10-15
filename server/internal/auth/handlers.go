// Package auth implements challenge/login/logout and session listing.
// Account bundle/rekey live in internal/account.
package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"runer-api/internal/httpx"
	"runer-api/internal/limits"
	"runer-api/internal/store"

	"github.com/labstack/echo/v5"
)

// DefaultChallengeTTL is the production nonce lifetime (E2EE §3.4).
const DefaultChallengeTTL = 60 * time.Second

// Limits are the per-IP budgets and signup difficulty (Self-hosting:
// Limits). Per-IP caps are loose flood guards, not the real anti-abuse
// measure: many people share one IP (offices, mobile carrier NAT, VPNs),
// so the proof-of-work is what makes each account cost something.
type Limits struct {
	ChallengesPerIPPerMinute int
	SignupsPerIPPerHour      int
	SignupBurstPerIP         int
	SignupPoWBits            int
}

// DefaultLimits are the production defaults.
var DefaultLimits = Limits{
	ChallengesPerIPPerMinute: 120,
	SignupsPerIPPerHour:      100,
	SignupBurstPerIP:         20,
	SignupPoWBits:            limits.DefaultSignupPoWBits,
}

type Handler struct {
	accounts     *store.Accounts
	sessions     *store.Sessions
	serverURL    string // canonical API URL covered by login signatures
	challenges   *challengeStore
	challengeIPs *limits.Keyed // /auth/challenge per IP
	signupIPs    *limits.Keyed // signup (first login) per IP
	powBits      int           // advertised in every challenge response
	conns        ConnCloser
	parseGenesis GenesisParser
}

// GenesisParser checks an Inbox genesis for the identity being registered
// (grouplog.ParseGenesis; passed in because grouplog imports auth).
type GenesisParser func(raw, sig, signPub, agreePub []byte) (store.LogEntry, error)

// NewHandler builds the auth handler. challengeTTL is a constructor
// parameter (rather than a package constant) so tests can inject a short
// TTL and exercise real expiry without sleeping 60s; production always
// passes DefaultChallengeTTL.
func NewHandler(accounts *store.Accounts, sessions *store.Sessions, serverURL string, challengeTTL time.Duration, lim Limits, conns ConnCloser, parseGenesis GenesisParser) *Handler {
	return &Handler{
		accounts:     accounts,
		sessions:     sessions,
		serverURL:    serverURL,
		challenges:   newChallengeStore(challengeTTL),
		challengeIPs: limits.PerMinute(lim.ChallengesPerIPPerMinute),
		signupIPs:    limits.NewKeyed(float64(lim.SignupsPerIPPerHour)/3600, lim.SignupBurstPerIP),
		powBits:      lim.SignupPoWBits,
		conns:        conns,
		parseGenesis: parseGenesis,
	}
}

// RegisterRoutes mounts /auth/* under g and /sessions under session-protected g.
func (h *Handler) RegisterRoutes(g *echo.Group, requireSession echo.MiddlewareFunc) {
	g.POST("/auth/challenge", h.challenge)
	g.POST("/auth/login", h.login)
	g.POST("/auth/logout", h.logout, requireSession)
	g.GET("/sessions", h.listSessions, requireSession)
	g.DELETE("/sessions/:id", h.revokeSession, requireSession)
}

type challengeRequest struct {
	AuthPub string `json:"auth_pub" validate:"required"`
}

func (h *Handler) challenge(c *echo.Context) error {
	if !h.challengeIPs.Allow(c.RealIP()) {
		return ErrRateLimited
	}

	var req challengeRequest
	if err := httpx.BindAndValidate(c, &req); err != nil {
		return err
	}
	authPub, err := httpx.DecodeEd25519Key(req.AuthPub)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid auth_pub")
	}

	nonce, err := h.challenges.issue(base64.RawURLEncoding.EncodeToString(authPub))
	if err != nil {
		return err
	}
	return httpx.JSONData(c, http.StatusOK, map[string]any{
		"nonce":    base64.RawURLEncoding.EncodeToString(nonce),
		"pow_bits": h.powBits,
	})
}

type loginRequest struct {
	AuthPub string `json:"auth_pub" validate:"required"`
	Nonce   string `json:"nonce" validate:"required"`
	Sig     string `json:"sig" validate:"required"`

	// Signup-only fields. Present together, or not at all.
	IdentitySignPub  string `json:"identity_sign_pub"`
	IdentityAgreePub string `json:"identity_agree_pub"`
	IdentityBundle   string `json:"identity_bundle"`
	IdentitySig      string `json:"identity_sig"` // IdentityMessage signed with the identity key
	PowNonce         string `json:"pow_nonce"`
	InboxEntry       string `json:"inbox_entry"` // the Inbox's signed create entry
	InboxSig         string `json:"inbox_sig"`
}

func (h *Handler) login(c *echo.Context) error {
	var req loginRequest
	if err := httpx.BindAndValidate(c, &req); err != nil {
		return err
	}

	authPub, err := httpx.DecodeEd25519Key(req.AuthPub)
	if err != nil {
		return ErrInvalidCredentials
	}
	nonce, err := httpx.DecodeKey(req.Nonce, 32)
	if err != nil {
		return ErrInvalidCredentials
	}
	sig, err := base64.RawURLEncoding.DecodeString(req.Sig)
	if err != nil {
		return ErrInvalidCredentials
	}

	if !h.challenges.consume(base64.RawURLEncoding.EncodeToString(authPub), nonce) {
		return ErrInvalidCredentials
	}
	if !ed25519.Verify(authPub, LoginMessage(h.serverURL, nonce), sig) {
		return ErrInvalidCredentials
	}

	ctx := c.Request().Context()
	acc, err := h.accounts.GetByAuthPub(ctx, authPub)
	switch err {
	case nil:
		// Existing account: plain login.
	case store.ErrNotFound:
		acc, err = h.signup(c, authPub, nonce, req)
		if err != nil {
			return err
		}
	default:
		return err
	}

	token, hash, err := newSessionToken()
	if err != nil {
		return err
	}
	sess, err := h.sessions.Create(ctx, acc.ID, authPub, hash, c.Request().UserAgent())
	if errors.Is(err, store.ErrNotFound) {
		return ErrInvalidCredentials // rekeyed or deleted since the lookup above
	}
	if err != nil {
		return err
	}
	setSessionCookie(c.Response(), token, int(store.SessionAbsoluteTimeout.Seconds()))

	return httpx.JSONData(c, http.StatusOK, map[string]any{
		"account_id": acc.ID,
		"bundle_gen": acc.BundleGen,
		"session_id": sess.ID,
		"inbox_id":   acc.InboxID,
	})
}

// signup handles first-login account creation. Requires identity fields and
// a valid proof-of-work (E2EE §3.3).
func (h *Handler) signup(c *echo.Context, authPub, nonce []byte, req loginRequest) (store.Account, error) {
	if !h.signupIPs.Allow(c.RealIP()) {
		slog.Warn("signup rate-limited") // a burst of these is a flood signal
		return store.Account{}, ErrRateLimited
	}
	if req.IdentitySignPub == "" || req.IdentityAgreePub == "" || req.IdentityBundle == "" {
		return store.Account{}, ErrInvalidCredentials
	}

	identitySignPub, err := httpx.DecodeEd25519Key(req.IdentitySignPub)
	if err != nil {
		return store.Account{}, ErrInvalidCredentials
	}
	identityAgreePub, err := httpx.DecodeKey(req.IdentityAgreePub, httpx.X25519KeySize)
	if err != nil {
		return store.Account{}, ErrInvalidCredentials
	}
	identityBundle, err := base64.RawURLEncoding.DecodeString(req.IdentityBundle)
	if err != nil {
		return store.Account{}, ErrInvalidCredentials
	}
	identitySig, err := base64.RawURLEncoding.DecodeString(req.IdentitySig)
	if err != nil || !ed25519.Verify(identitySignPub, IdentityMessage(h.serverURL, nonce), identitySig) {
		return store.Account{}, ErrInvalidCredentials
	}
	powNonce, err := base64.RawURLEncoding.DecodeString(req.PowNonce)
	if err != nil || !limits.VerifyProofOfWork(authPub, nonce, powNonce, h.powBits) {
		return store.Account{}, ErrInvalidCredentials
	}

	inbox, err := h.parseInbox(req.InboxEntry, req.InboxSig, identitySignPub, identityAgreePub)
	if err != nil {
		return store.Account{}, err
	}

	acc, err := h.accounts.Create(c.Request().Context(), authPub, identitySignPub, identityAgreePub, identityBundle, inbox)
	if errors.Is(err, store.ErrConflict) {
		return store.Account{}, echo.NewHTTPError(http.StatusConflict, "already exists")
	}
	if err == nil {
		slog.Info("signup") // one line per account: count these to spot a flood
	}
	return acc, err
}

// parseInbox decodes and checks an Inbox genesis by the given identity.
// Anything wrong with it is 400: the caller already proved the keys.
func (h *Handler) parseInbox(entry, sig string, signPub, agreePub []byte) (store.LogEntry, error) {
	raw, rawErr := base64.RawURLEncoding.DecodeString(entry)
	sigBytes, sigErr := base64.RawURLEncoding.DecodeString(sig)
	if entry == "" || sig == "" || rawErr != nil || sigErr != nil {
		return store.LogEntry{}, echo.NewHTTPError(http.StatusBadRequest, "missing or invalid inbox entry")
	}
	inbox, err := h.parseGenesis(raw, sigBytes, signPub, agreePub)
	if err != nil {
		return store.LogEntry{}, echo.NewHTTPError(http.StatusBadRequest, "invalid inbox entry")
	}
	return inbox, nil
}

func (h *Handler) logout(c *echo.Context) error {
	sess, ok := SessionFromContext(c)
	if !ok {
		return ErrUnauthenticated
	}
	if err := h.sessions.Revoke(c.Request().Context(), sess.ID, sess.AccountID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	h.conns.CloseSessionConnections(sess.AccountID, sess.ID)
	clearSessionCookie(c.Response())
	return httpx.JSONData(c, http.StatusOK, map[string]any{"revoked": true})
}

type sessionView struct {
	ID         string    `json:"id"`
	UserAgent  *string   `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	// The session making this request, so a client can mark "this device".
	Current bool `json:"current"`
}

func (h *Handler) listSessions(c *echo.Context) error {
	acc, _ := AccountFromContext(c)
	self, _ := SessionFromContext(c)
	sessions, err := h.sessions.ListByAccount(c.Request().Context(), acc.ID)
	if err != nil {
		return err
	}
	views := make([]sessionView, len(sessions))
	for i, s := range sessions {
		views[i] = sessionView{ID: s.ID, UserAgent: s.UserAgent, CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt, Current: s.ID == self.ID}
	}
	return httpx.JSONData(c, http.StatusOK, views)
}

func (h *Handler) revokeSession(c *echo.Context) error {
	acc, _ := AccountFromContext(c)
	id := c.Param("id")
	if err := h.sessions.Revoke(c.Request().Context(), id, acc.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "not found")
		}
		return err
	}
	h.conns.CloseSessionConnections(acc.ID, id)
	return httpx.JSONData(c, http.StatusOK, map[string]any{"revoked": true})
}
