// Package config loads server configuration from environment variables.
// Config is never hardcoded.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// ListenAddr is the address the HTTP server binds, e.g. ":8080".
	ListenAddr string
	// DatabaseURL is the PostgreSQL connection string.
	DatabaseURL string
	// Origin is the single Origin header this server accepts on mutating
	// requests and the WebSocket upgrade (E2EE §3.4).
	Origin string
	// PublicURL is this server's canonical API base URL, e.g.
	// "https://api.runer.app". Login and rekey signatures cover it, so a
	// signature made for one server can't be relayed to another.
	PublicURL string
	// TrustedProxies are the peer ranges whose X-Forwarded-For is honoured
	// when deriving the client IP for per-IP limits. Empty by default: no
	// peer is trusted, so a client reaching the port directly can't spoof
	// its IP. Behind a reverse proxy, set it to exactly the proxy's
	// address or network, or every client shares the proxy's IP bucket.
	TrustedProxies []*net.IPNet

	// Per-IP budgets and signup difficulty (Self-hosting: Limits). Zero
	// means the server's default.
	ChallengesPerIPPerMinute  int
	SignupsPerIPPerHour       int
	SignupBurstPerIP          int
	SignupPoWBits             int
	InviteReadsPerIPPerMinute int
}

// PoW difficulty bounds: below 8 bits the work is trivial, above 28 a
// phone takes minutes.
const (
	MinSignupPoWBits = 8
	MaxSignupPoWBits = 28
)

// Load reads configuration from the environment. Required: DATABASE_URL,
// ORIGIN, PUBLIC_URL. Optional: LISTEN_ADDR (default ":8080"),
// TRUSTED_PROXIES (comma-separated CIDRs, default none), and the limits
// below (docs/self-hosting.md).
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:  envOr("LISTEN_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Origin:      os.Getenv("ORIGIN"),
		PublicURL:   strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.Origin == "" {
		return Config{}, fmt.Errorf("ORIGIN is required")
	}
	if cfg.PublicURL == "" {
		return Config{}, fmt.Errorf("PUBLIC_URL is required")
	}
	proxies, err := ParseCIDRs(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return Config{}, fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	cfg.TrustedProxies = proxies

	for _, v := range []struct {
		name string
		dst  *int
		min  int
		max  int
	}{
		{"CHALLENGES_PER_IP_PER_MINUTE", &cfg.ChallengesPerIPPerMinute, 1, 1_000_000},
		{"SIGNUPS_PER_IP_PER_HOUR", &cfg.SignupsPerIPPerHour, 1, 1_000_000},
		{"SIGNUP_BURST_PER_IP", &cfg.SignupBurstPerIP, 1, 1_000_000},
		{"SIGNUP_POW_BITS", &cfg.SignupPoWBits, MinSignupPoWBits, MaxSignupPoWBits},
		{"INVITE_READS_PER_IP_PER_MINUTE", &cfg.InviteReadsPerIPPerMinute, 1, 1_000_000},
	} {
		if *v.dst, err = intEnv(v.name, v.min, v.max); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

// intEnv reads an optional integer in [min, max]; unset is 0 (the default).
func intEnv(name string, min, max int) (int, error) {
	s := os.Getenv(name)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, min, max)
	}
	return n, nil
}

// ParseCIDRs parses a comma-separated CIDR list.
func ParseCIDRs(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
