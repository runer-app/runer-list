package limits

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// sweepInterval is how often Keyed drops idle limiters.
const sweepInterval = time.Minute

// Keyed is a set of per-key token-bucket rate limiters, e.g. one per client
// IP.
//
// In-process, so with several instances each allows the full budget:
// fine for the per-IP flood guards (Self-hosting: Shape of a deployment).
// A limit that must hold across instances lives in the database instead
// (the join limit per invite). Keys are attacker-chosen (IPs, ids), so
// idle limiters are swept: a bucket back at full burst is identical to a
// fresh one, so dropping it changes no decision.
type Keyed struct {
	mu        sync.Mutex
	limiters  map[string]*rate.Limiter
	r         rate.Limit
	burst     int
	lastSweep time.Time
}

// NewKeyed creates a limiter allowing burst immediate calls per key, then
// perSecond sustained.
func NewKeyed(perSecond float64, burst int) *Keyed {
	return &Keyed{limiters: make(map[string]*rate.Limiter), r: rate.Limit(perSecond), burst: burst}
}

// Allow reports whether the call for key is within budget right now.
func (k *Keyed) Allow(key string) bool {
	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	if now.Sub(k.lastSweep) >= sweepInterval {
		for key, l := range k.limiters {
			if l.TokensAt(now) >= float64(k.burst) {
				delete(k.limiters, key)
			}
		}
		k.lastSweep = now
	}
	l, ok := k.limiters[key]
	if !ok {
		l = rate.NewLimiter(k.r, k.burst)
		k.limiters[key] = l
	}
	return l.AllowN(now, 1)
}

// PerMinute is a convenience constructor for an N-per-minute budget.
func PerMinute(n int) *Keyed {
	return NewKeyed(float64(n)/60, n)
}

// PerHour is a convenience constructor for an N-per-hour budget.
func PerHour(n int) *Keyed {
	return NewKeyed(float64(n)/3600, n)
}
