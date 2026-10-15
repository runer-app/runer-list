package invite

import "errors"

var (
	// ErrForbidden -> 403: caller is not the owner of the invite's list.
	ErrForbidden = errors.New("forbidden")
	// ErrNotUsable -> 410: invite is expired, revoked, or already consumed
	// (E2EE §6: "Invites expire after 7 days and are single-use").
	ErrNotUsable = errors.New("invite is no longer usable")
	// ErrTooManyJoinRequests -> 429: per-invite join-request rate limit
	// (Self-hosting: Limits: 10 per invite).
	ErrTooManyJoinRequests = errors.New("too many join requests for this invite")
	// ErrExpiryTooFar -> 400: expires_at exceeds the 7-day maximum.
	ErrExpiryTooFar = errors.New("expires_at exceeds the 7-day maximum")
	// ErrNoLogInvite -> 409: a link invite's package names an invite key
	// with no open `invite` entry in the list's log, or one expiring
	// before expires_at.
	ErrNoLogInvite = errors.New("no open invite entry for invite_pub")
)
