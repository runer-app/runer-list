// Command server runs the runer list server: one binary, one
// container (docs/self-hosting.md).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"runer-api/internal/auth"
	"runer-api/internal/config"
	"runer-api/internal/retention"
	"runer-api/internal/server"
	"runer-api/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	lim := auth.DefaultLimits
	setIf(&lim.ChallengesPerIPPerMinute, cfg.ChallengesPerIPPerMinute)
	setIf(&lim.SignupsPerIPPerHour, cfg.SignupsPerIPPerHour)
	setIf(&lim.SignupBurstPerIP, cfg.SignupBurstPerIP)
	setIf(&lim.SignupPoWBits, cfg.SignupPoWBits)
	slog.Info("limits", "challenges_per_ip_per_minute", lim.ChallengesPerIPPerMinute,
		"signups_per_ip_per_hour", lim.SignupsPerIPPerHour, "signup_burst_per_ip", lim.SignupBurstPerIP,
		"signup_pow_bits", lim.SignupPoWBits)

	e := server.New(pool, server.Options{
		Origin: cfg.Origin, PublicURL: cfg.PublicURL, TrustedProxies: cfg.TrustedProxies,
		Limits: lim, InviteReadsPerIPPerMinute: cfg.InviteReadsPerIPPerMinute,
	})
	go runRetentionLoop(ctx, pool)

	// Blocks until ctx is cancelled, then shuts down gracefully (10s).
	return echo.StartConfig{
		Address:         cfg.ListenAddr,
		HideBanner:      true,
		GracefulTimeout: 10 * time.Second,
		BeforeServeFunc: func(s *http.Server) error {
			// No Read/WriteTimeout: they would also cut long-lived WebSockets.
			// Header and idle timeouts stop slow-header and idle-connection hoarding.
			s.ReadHeaderTimeout = 10 * time.Second
			s.IdleTimeout = 120 * time.Second
			return nil
		},
	}.Start(ctx, e)
}

// runRetentionLoop runs the retention purge job once at startup and then
// daily until ctx is done.
// setIf overrides a default with a configured value; 0 means unset.
func setIf(dst *int, v int) {
	if v != 0 {
		*dst = v
	}
}

func runRetentionLoop(ctx context.Context, pool *pgxpool.Pool) {
	purger := retention.NewPurger(pool, retention.RealClock{}, retention.DefaultWindow)

	purge := func() {
		report, err := purger.PurgeOnce(ctx)
		if err != nil {
			slog.Error("retention purge failed", "error", err)
			return
		}
		slog.Info("retention purge complete", "groups", report.Groups, "accounts", report.Accounts, "sessions", report.Sessions)
	}

	purge()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purge()
		}
	}
}
