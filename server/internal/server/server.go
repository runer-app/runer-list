// Package server wires the Go application: middleware, routes, error
// handling. cmd/server/main.go only calls New and starts it.
package server

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"runer-api/internal/account"
	"runer-api/internal/auth"
	"runer-api/internal/grouplog"
	"runer-api/internal/invite"
	"runer-api/internal/store"
	syncpkg "runer-api/internal/sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

// Options configures New. Origin, PublicURL and TrustedProxies come from
// config.Config in production.
type Options struct {
	// Origin is the only Origin header accepted on mutations and the
	// WebSocket upgrade.
	Origin string
	// PublicURL is the canonical API URL login and rekey signatures cover.
	PublicURL string
	// TrustedProxies are the peers whose X-Forwarded-For is honoured.
	TrustedProxies []*net.IPNet
	// ChallengeTTL is the login-challenge lifetime; zero means
	// auth.DefaultChallengeTTL. Only tests change it.
	ChallengeTTL time.Duration
	// Limits are the per-IP budgets and signup difficulty; the zero value
	// means auth.DefaultLimits.
	Limits auth.Limits
	// InviteReadsPerIPPerMinute caps unauthenticated invite package reads;
	// zero means DefaultInviteReadsPerIPPerMinute.
	InviteReadsPerIPPerMinute int
}

// DefaultInviteReadsPerIPPerMinute is the default invite package read
// budget per IP.
const DefaultInviteReadsPerIPPerMinute = 30

// New builds a fully wired Echo instance. Middleware order: RequestID,
// Logger, Recover, body limit, security headers, then routes (Origin is
// checked on the /v1 group).
func New(pool *pgxpool.Pool, opts Options) *echo.Echo {
	if opts.ChallengeTTL == 0 {
		opts.ChallengeTTL = auth.DefaultChallengeTTL
	}
	if opts.Limits == (auth.Limits{}) {
		opts.Limits = auth.DefaultLimits
	}
	if opts.InviteReadsPerIPPerMinute == 0 {
		opts.InviteReadsPerIPPerMinute = DefaultInviteReadsPerIPPerMinute
	}
	origin := opts.Origin

	e := echo.New()
	e.HTTPErrorHandler = httpErrorHandler
	// Per-IP limits key on c.RealIP(). X-Forwarded-For is honoured only
	// when the direct peer is a configured trusted proxy (TRUSTED_PROXIES);
	// any other peer is keyed on its own address and can't spoof one.
	trust := []echo.TrustOption{echo.TrustLoopback(false), echo.TrustLinkLocal(false), echo.TrustPrivateNet(false)}
	for _, n := range opts.TrustedProxies {
		trust = append(trust, echo.TrustIPRange(n))
	}
	e.IPExtractor = echo.ExtractIPFromXFFHeader(trust...)

	e.Use(middleware.RequestID())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		// The route pattern, never the raw URI: paths carry credentials
		// (GET /invites/{invite_id} — the id IS the invite credential).
		LogStatus: true, LogRoutePath: true, LogMethod: true, LogLatency: true,
		// Run httpErrorHandler here so the logged status is the one sent,
		// not 500 for every domain error.
		HandleError: true,
		// Logs c.RealIP() so an operator can check, behind a reverse proxy,
		// that per-IP limits see real public client IPs, not the proxy's
		// 10.x/172.x addresses (which would mean every user shares one
		// bucket). Client IPs are personal data: see docs/self-hosting.md.
		LogRemoteIP: true,
		LogValuesFunc: func(c *echo.Context, v middleware.RequestLoggerValues) error {
			level := slog.LevelInfo
			if v.Error != nil {
				level = slog.LevelError
			}
			slog.Log(c.Request().Context(), level, "request",
				"method", v.Method, "route", v.RoutePath, "status", v.Status, "latency", v.Latency, "error", v.Error,
				"remote_ip", v.RemoteIP)
			return nil
		},
	}))
	e.Use(middleware.Recover())
	// Largest legitimate body is a log entry carrying one envelope per
	// member; 1 MiB leaves ample room. WebSocket frames have their own limits.
	e.Use(middleware.BodyLimit(1 << 20))
	// A JSON API: nothing may render, frame or embed it. HSTS is sent only
	// over TLS (or X-Forwarded-Proto: https from the proxy).
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "DENY",
		HSTSMaxAge:            31536000,
		ContentSecurityPolicy: "default-src 'none'; frame-ancestors 'none'",
		ReferrerPolicy:        "no-referrer",
	}))

	accounts := store.NewAccounts(pool)
	sessions := store.NewSessions(pool)
	groups := store.NewGroups(pool)
	invites := store.NewInvites(pool)
	updates := store.NewUpdates(pool)
	snapshots := store.NewSnapshots(pool)

	e.GET("/healthz", func(c *echo.Context) error {
		if err := pool.Ping(c.Request().Context()); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "database unavailable")
		}
		return c.String(http.StatusOK, "ok")
	})

	e.GET("/join", joinHandler)

	v1 := e.Group("/v1")
	v1.Use(auth.RequireOrigin(origin))

	requireSession := auth.RequireSession(sessions, accounts)

	// syncHandler is built first so it can be passed in as the Notifier
	// that closes a removed member's live connections, and as the
	// ConnCloser that ends sockets when their session ends.
	syncHandler := syncpkg.NewHandler(groups, accounts, sessions, updates, snapshots, origin)
	syncHandler.RegisterRoutes(v1, requireSession)

	authHandler := auth.NewHandler(accounts, sessions, opts.PublicURL, opts.ChallengeTTL, opts.Limits, syncHandler, grouplog.ParseGenesis)
	authHandler.RegisterRoutes(v1, requireSession)

	accountHandler := account.NewHandler(accounts, sessions, opts.PublicURL, syncHandler)
	accountHandler.RegisterRoutes(v1, requireSession)

	grouplogService := grouplog.NewService(groups, accounts, syncHandler)
	grouplogHandler := grouplog.NewHandler(grouplogService, groups)
	grouplogHandler.RegisterRoutes(v1, requireSession)

	inviteService := invite.NewService(invites, groups, grouplogService)
	inviteHandler := invite.NewHandler(inviteService, opts.InviteReadsPerIPPerMinute)
	inviteHandler.RegisterRoutes(v1, requireSession)

	return e
}
