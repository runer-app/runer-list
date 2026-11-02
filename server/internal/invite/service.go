// Package invite implements the invite-link join flow (E2EE §6). The
// server never sees the invite secret `s` — it stores and relays opaque
// bytes (package, enc_display_name, mac) and enforces only what it can:
// owner-only creation, single-use, expiry, revocation, and a per-invite
// rate limit.
package invite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"runer-api/internal/grouplog"
	"runer-api/internal/limits"
	"runer-api/internal/store"
)

const maxExpiry = 7 * 24 * time.Hour

// LogAppender appends a validated entry to a list's log (grouplog.Service).
type LogAppender interface {
	AppendEntry(ctx context.Context, callerAccountID, groupID string, raw, sig []byte) (store.Group, error)
}

type Service struct {
	invites    *store.Invites
	groups     *store.Groups
	log        LogAppender
	joinLimits *limits.Keyed // keyed by invite id, per Self-hosting: Limits
}

func NewService(invites *store.Invites, groups *store.Groups, log LogAppender) *Service {
	return &Service{
		invites: invites, groups: groups, log: log,
		joinLimits: limits.NewKeyed(10.0/86400, 10), // burst of 10 per invite, then 10 a day
	}
}

// requireOwner is the shared access check (live list, current member)
// plus the owner role.
func (s *Service) requireOwner(ctx context.Context, groupID, accountID string) error {
	_, role, err := s.groups.Access(ctx, groupID, accountID)
	if errors.Is(err, store.ErrForbidden) || (err == nil && role != "owner") {
		return ErrForbidden
	}
	return err
}

// CreateInvite stores an owner-created invite to a list. id and pkg are
// opaque, client-derived/encrypted values (E2EE §6); the server only
// enforces the expiry ceiling and owner authorization. For a link invite,
// invitePub names the key an open `invite` entry in the list's log opened.
func (s *Service) CreateInvite(ctx context.Context, groupID, callerAccountID, id string, pkg []byte, expiresAt time.Time, invitePub []byte) (store.Invite, error) {
	if err := s.requireOwner(ctx, groupID, callerAccountID); err != nil {
		return store.Invite{}, err
	}
	if expiresAt.After(time.Now().Add(maxExpiry)) {
		return store.Invite{}, ErrExpiryTooFar
	}
	inv, err := s.invites.Create(ctx, id, groupID, callerAccountID, pkg, expiresAt, invitePub)
	if errors.Is(err, store.ErrForbidden) {
		return store.Invite{}, ErrForbidden // list deleted since requireOwner
	}
	if errors.Is(err, store.ErrInviteNotUsable) {
		return store.Invite{}, ErrNoLogInvite
	}
	return inv, err
}

// GetPackage returns the invite's opaque package. Available to anyone who
// holds the invite id (that's the whole trust model — see E2EE §6),
// except once revoked or past expiry: those stop leaking package
// bytes at all. Consumed alone does not block reading it back; the single-
// use gate is Join, not this.
func (s *Service) GetPackage(ctx context.Context, id string) ([]byte, error) {
	inv, err := s.invites.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if inv.RevokedAt != nil || time.Now().After(inv.ExpiresAt) {
		return nil, ErrNotUsable
	}
	// A link invite's package holds the list key: once joined, nobody else
	// gets it.
	if inv.InvitePub != nil && inv.ConsumedAt != nil {
		return nil, ErrNotUsable
	}
	return inv.Package, nil
}

// linkInvite returns a usable link invite, the invitee's only credential
// before they are a member.
func (s *Service) linkInvite(ctx context.Context, inviteID string) (store.Invite, error) {
	inv, err := s.invites.Get(ctx, inviteID)
	if err != nil {
		return store.Invite{}, err
	}
	if inv.InvitePub == nil || !inv.Usable(time.Now()) {
		return store.Invite{}, ErrNotUsable
	}
	return inv, nil
}

// InviteLog returns the invite's list log from fromSeq, so the invitee
// can verify it from genesis and build a `join` on the current head
// (E2EE §6).
func (s *Service) InviteLog(ctx context.Context, inviteID string, fromSeq int64, limit int) ([]store.LogEntry, error) {
	inv, err := s.linkInvite(ctx, inviteID)
	if err != nil {
		return nil, err
	}
	return s.groups.ListLogFrom(ctx, inv.GroupID, fromSeq, limit)
}

// Accept appends the invitee's own `join` entry. The log rules decide
// whether it is valid; this only checks it is a join for THIS invite.
func (s *Service) Accept(ctx context.Context, inviteID, accountID string, raw, sig []byte) (store.Group, error) {
	if !s.joinLimits.Allow(inviteID) {
		return store.Group{}, ErrTooManyJoinRequests
	}
	inv, err := s.linkInvite(ctx, inviteID)
	if err != nil {
		return store.Group{}, err
	}
	entry, err := grouplog.Parse(raw, sig)
	if err != nil {
		return store.Group{}, err
	}
	if entry.Type != grouplog.TypeJoin || !bytes.Equal(entry.Invite, inv.InvitePub) {
		return store.Group{}, fmt.Errorf("%w: not a join for this invite", grouplog.ErrMalformed)
	}
	return s.log.AppendEntry(ctx, accountID, inv.GroupID, raw, sig)
}

// Join records a join request. The caller's identity is whatever the
// session already has registered (from signup) — there is no reason to
// let the client resubmit identity keys the server can't verify anyway;
// the owner verifies the MAC against the SAME registered keys via
// ListJoinRequests.
func (s *Service) Join(ctx context.Context, inviteID, accountID string, encDisplayName, mac []byte) (store.JoinRequest, error) {
	if !s.joinLimits.Allow(inviteID) {
		return store.JoinRequest{}, ErrTooManyJoinRequests
	}

	inv, err := s.invites.Get(ctx, inviteID)
	if err != nil {
		return store.JoinRequest{}, err
	}
	if !inv.Usable(time.Now()) {
		return store.JoinRequest{}, ErrNotUsable
	}

	jr, err := s.invites.CreateJoinRequest(ctx, inviteID, accountID, encDisplayName, mac)
	if errors.Is(err, store.ErrInviteNotUsable) {
		return store.JoinRequest{}, ErrNotUsable // revoked or group deleted since the check above
	}
	return jr, err
}

// ListJoinRequests returns join requests the owner can still approve, with
// each requester's registered identity keys — what the owner needs to
// verify the MAC and show a safety code (E2EE §6).
func (s *Service) ListJoinRequests(ctx context.Context, groupID, callerAccountID string) ([]store.PendingJoinRequest, error) {
	if err := s.requireOwner(ctx, groupID, callerAccountID); err != nil {
		return nil, err
	}
	return s.invites.ListJoinRequestsForGroup(ctx, groupID)
}

func (s *Service) Revoke(ctx context.Context, inviteID, callerAccountID string) error {
	inv, err := s.invites.Get(ctx, inviteID)
	if err != nil {
		return err
	}
	if err := s.requireOwner(ctx, inv.GroupID, callerAccountID); err != nil {
		return err
	}
	return s.invites.Revoke(ctx, inviteID, inv.GroupID)
}
