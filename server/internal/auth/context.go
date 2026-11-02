package auth

import (
	"runer-api/internal/store"

	"github.com/labstack/echo/v5"
)

// *echo.Context storage is a plain map[string]any, so descriptive string
// keys (rather than the context.Context typed-key trick) are the idiomatic
// choice here.
const (
	accountContextKey = "auth.account"
	sessionContextKey = "auth.session"
)

// AccountFromContext returns the authenticated account attached by
// Middleware, if any.
func AccountFromContext(c *echo.Context) (store.Account, bool) {
	acc, ok := c.Get(accountContextKey).(store.Account)
	return acc, ok
}

// SessionFromContext returns the authenticated session attached by
// Middleware, if any.
func SessionFromContext(c *echo.Context) (store.Session, bool) {
	sess, ok := c.Get(sessionContextKey).(store.Session)
	return sess, ok
}
