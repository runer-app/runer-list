package auth

import (
	"context"
	"net/http"
	"time"

	"runer-api/internal/store"

	"github.com/labstack/echo/v5"
)

// ConnCloser closes live sync connections when sessions end, so logout,
// revocation, rekey and account deletion also cut off open WebSockets
// promptly. Implemented by the sync handler.
// Closed sockets get `bye` with reason "session-revoked" (Sync §3), on
// which the client does not retry.
type ConnCloser interface {
	CloseAccountSessions(ctx context.Context, accountID string)
	CloseSessionConnections(accountID, sessionID string)
	CloseOtherSessionConnections(accountID, keepSessionID string)
}

// RequireOrigin rejects mutating requests (state-changing methods) whose
// Origin header is missing or does not match the configured origin
// (E2EE §3.4: "Missing or wrong Origin on a mutation → rejected").
func RequireOrigin(origin string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if isMutating(c.Request().Method) {
				if c.Request().Header.Get("Origin") != origin {
					return ErrOriginMismatch
				}
			}
			return next(c)
		}
	}
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// RequireSession loads the session from the cookie, rejects if missing,
// unknown, expired or revoked, and attaches the account and session to the
// request context. It also refreshes last_seen_at on both rows.
func RequireSession(sessions *store.Sessions, accounts *store.Accounts) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			cookie, err := c.Cookie(sessionCookieName)
			if err != nil || cookie.Value == "" {
				return ErrUnauthenticated
			}
			hash, err := hashSessionToken(cookie.Value)
			if err != nil {
				return ErrUnauthenticated
			}

			ctx := c.Request().Context()
			sess, err := sessions.GetByTokenHash(ctx, hash)
			if err != nil {
				return ErrUnauthenticated
			}
			if sess.RevokedAt != nil {
				return ErrSessionRevoked
			}
			if sess.Expired(time.Now()) {
				return ErrUnauthenticated
			}
			acc, err := accounts.GetByID(ctx, sess.AccountID)
			if err != nil {
				return ErrUnauthenticated
			}

			_ = sessions.TouchLastSeen(ctx, sess.ID)
			_ = accounts.TouchLastSeen(ctx, acc.ID)

			c.Set(sessionContextKey, sess)
			c.Set(accountContextKey, acc)
			return next(c)
		}
	}
}
