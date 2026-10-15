package auth

import "errors"

var (
	// ErrInvalidCredentials covers every login failure: unknown auth_pub,
	// bad signature, bad/expired/reused challenge, or a signup attempt
	// missing required fields. E2EE §3.4: "the server answers identically
	// whether or not the account exists" — one error, one status code, no
	// way to distinguish "wrong words" from "no such account".
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrOriginMismatch is returned when a mutating request's Origin header
	// is missing or does not match the configured origin (E2EE §3.4).
	ErrOriginMismatch = errors.New("origin mismatch")

	// ErrUnauthenticated is returned when a session cookie is missing,
	// unknown or expired. The client signs in again with its key.
	ErrUnauthenticated = errors.New("unauthenticated")

	// ErrSessionRevoked is returned for a session someone ended (logout,
	// revocation, rekey elsewhere). Unlike ErrUnauthenticated, the client
	// must not sign back in on its own: it stops and asks the user.
	ErrSessionRevoked = errors.New("session revoked")

	// ErrRateLimited is returned when a per-IP budget is exhausted.
	ErrRateLimited = errors.New("rate limited")
)
