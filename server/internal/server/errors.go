package server

import (
	"errors"
	"log/slog"
	"net/http"

	"runer-api/internal/auth"
	"runer-api/internal/grouplog"
	"runer-api/internal/invite"
	"runer-api/internal/store"

	"github.com/labstack/echo/v5"
)

// httpErrorHandler maps domain sentinel errors to HTTP status codes and
// never leaks internal error text or stack traces to the client.
func httpErrorHandler(c *echo.Context, err error) {
	if r, _ := echo.UnwrapResponse(c.Response()); r != nil && r.Committed {
		return
	}

	status, code, msg := classify(err)
	if status >= http.StatusInternalServerError {
		slog.Error("request failed", "error", err, "path", c.Path(), "method", c.Request().Method)
	}

	resp := map[string]any{"error": map[string]any{"code": code}}
	if msg != "" {
		resp["error"].(map[string]any)["message"] = msg
	}
	if err := c.JSON(status, resp); err != nil {
		slog.Error("write error response", "error", err)
	}
}

func classify(err error) (status int, code, msg string) {
	if he, ok := errors.AsType[*echo.HTTPError](err); ok {
		return he.Code, statusCode(he.Code), he.Message
	}
	// Echo's own errors (404/405 routing, 413 body limit) are an unexported
	// type that only exposes StatusCode.
	if s := echo.StatusCode(err); s != 0 {
		return s, statusCode(s), ""
	}

	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		return http.StatusUnauthorized, "invalid_credentials", ""
	case errors.Is(err, auth.ErrUnauthenticated):
		return http.StatusUnauthorized, "unauthenticated", ""
	case errors.Is(err, auth.ErrOriginMismatch):
		return http.StatusForbidden, "origin_mismatch", ""
	case errors.Is(err, auth.ErrRateLimited):
		return http.StatusTooManyRequests, "rate_limited", ""
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not_found", ""
	case errors.Is(err, store.ErrForbidden):
		return http.StatusForbidden, "forbidden", ""
	case errors.Is(err, store.ErrAccountHasLists):
		return http.StatusConflict, "account_has_lists", ""
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, "conflict", ""
	case errors.Is(err, grouplog.ErrConflict):
		return http.StatusConflict, "conflict", ""
	case errors.Is(err, grouplog.ErrInviteNotOpen), errors.Is(err, grouplog.ErrInviteExpired):
		return http.StatusGone, "invite_not_usable", ""
	case errors.Is(err, grouplog.ErrForbidden):
		return http.StatusForbidden, "forbidden", ""
	case errors.Is(err, grouplog.ErrMalformed):
		return http.StatusBadRequest, "bad_request", ""
	case errors.Is(err, invite.ErrForbidden):
		return http.StatusForbidden, "forbidden", ""
	case errors.Is(err, invite.ErrNotUsable):
		return http.StatusGone, "invite_not_usable", ""
	case errors.Is(err, invite.ErrTooManyJoinRequests):
		return http.StatusTooManyRequests, "rate_limited", ""
	case errors.Is(err, invite.ErrNoLogInvite):
		return http.StatusConflict, "conflict", ""
	case errors.Is(err, invite.ErrExpiryTooFar):
		return http.StatusBadRequest, "bad_request", ""
	default:
		return http.StatusInternalServerError, "internal", ""
	}
}

func statusCode(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthenticated"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusRequestEntityTooLarge:
		return "too_large"
	default:
		return "error"
	}
}
