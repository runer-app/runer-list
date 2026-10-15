package grouplog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"runer-api/internal/store"
)

// Notifier lets the sync layer react to membership changes: close a
// removed/left member's live connections (E2EE §8.1, "close Bob's
// sockets") and tell members the log grew (Sync §3, `group`).
type Notifier interface {
	CloseAccountConnections(ctx context.Context, accountID, reason string)
	NotifyGroup(ctx context.Context, groupID string, headSeq int64, accountIDs []string)
}

// Service validates and persists membership log entries with a CAS append.
type Service struct {
	groups   *store.Groups
	accounts *store.Accounts
	notifier Notifier // nil is fine: skips live-connection side effects (e.g. in tests)
}

func NewService(groups *store.Groups, accounts *store.Accounts, notifier Notifier) *Service {
	return &Service{groups: groups, accounts: accounts, notifier: notifier}
}

// CreateGroup validates a genesis entry and creates the list. Enforced
// here: the author is the caller, and author_agree is the caller's
// registered encryption key.
func (s *Service) CreateGroup(ctx context.Context, callerAccountID string, raw, sig []byte) (store.Group, error) {
	caller, err := s.accounts.GetByID(ctx, callerAccountID)
	if err != nil {
		return store.Group{}, err
	}
	genesis, err := ParseGenesis(raw, sig, caller.IdentitySignPub, caller.IdentityAgreePub)
	if err != nil {
		return store.Group{}, err
	}
	grp, err := s.groups.CreateGenesis(ctx, genesis, caller.ID, caller.IdentitySignPub)
	if err != nil {
		return store.Group{}, err
	}
	// The creator's other devices learn of the list without reconnecting
	// (Sync §3, `group`). The creator is its only member.
	if s.notifier != nil {
		s.notifier.NotifyGroup(ctx, grp.ID, grp.HeadSeq, []string{caller.ID})
	}
	return grp, nil
}

// ParseGenesis checks a create entry as POST /groups does, for an author
// with these keys: author = signPub, author_agree = agreePub. Sign-up uses
// it for the Inbox, before the keys are registered.
func ParseGenesis(raw, sig, signPub, agreePub []byte) (store.LogEntry, error) {
	entry, err := Parse(raw, sig)
	if err != nil {
		return store.LogEntry{}, err
	}
	if entry.Type != TypeCreate {
		return store.LogEntry{}, fmt.Errorf("%w: first entry must be create", ErrMalformed)
	}
	if err := checkNotFuture(entry); err != nil {
		return store.LogEntry{}, err
	}
	if !bytes.Equal(entry.Author, signPub) {
		return store.LogEntry{}, ErrAuthorNotCaller
	}
	if !bytes.Equal(entry.AuthorAgree, agreePub) {
		return store.LogEntry{}, ErrAgreeKeyMismatch
	}
	next, err := Validate(State{}, entry)
	if err != nil {
		return store.LogEntry{}, err
	}
	if err := checkSizes(entry, next); err != nil {
		return store.LogEntry{}, err
	}
	return logEntry(next.ListID, entry), nil
}

// AppendEntry validates a non-genesis entry against the group's current
// mirrored state and appends it (compare-and-swap on the head). The entry's
// author must be the caller's current identity.
func (s *Service) AppendEntry(ctx context.Context, callerAccountID, groupID string, raw, sig []byte) (store.Group, error) {
	entry, err := Parse(raw, sig)
	if err != nil {
		return store.Group{}, err
	}
	if entry.Type == TypeCreate {
		return store.Group{}, fmt.Errorf("%w: create is only valid via CreateGroup", ErrMalformed)
	}
	if entry.Group != groupID {
		return store.Group{}, fmt.Errorf("%w: entry.group does not match the URL group id", ErrMalformed)
	}
	if err := checkNotFuture(entry); err != nil {
		return store.Group{}, err
	}
	caller, err := s.accounts.GetByID(ctx, callerAccountID)
	if err != nil {
		return store.Group{}, err
	}
	if !bytes.Equal(caller.IdentitySignPub, entry.Author) {
		return store.Group{}, ErrAuthorNotCaller
	}

	grp, err := s.groups.Get(ctx, groupID)
	if err != nil {
		return store.Group{}, err
	}
	if entry.Type == TypeAdd || entry.Type == TypeInvite {
		if inbox, err := s.groups.IsInbox(ctx, groupID); err != nil {
			return store.Group{}, err
		} else if inbox {
			return store.Group{}, ErrInboxPrivate
		}
	}
	head, err := s.groups.GetLogEntry(ctx, groupID, grp.HeadSeq)
	if err != nil {
		return store.Group{}, err
	}

	members, err := s.loadMembers(ctx, groupID)
	if err != nil {
		return store.Group{}, err
	}
	invites, err := s.groups.ListInvites(ctx, groupID)
	if err != nil {
		return store.Group{}, err
	}
	state := State{
		Exists: true, ListID: groupID,
		HeadSeq: grp.HeadSeq, HeadHash: grp.HeadHash, Epoch: grp.Epoch,
		LastTs:  head.Ts,
		Members: rolesByIdentity(members),
		Invites: invitesByKey(invites),
	}
	next, err := Validate(state, entry)
	if err != nil {
		return store.Group{}, err
	}
	if err := checkSizes(entry, next); err != nil {
		return store.Group{}, err
	}
	if err := checkInviteClock(entry, state); err != nil {
		return store.Group{}, err
	}

	diff, err := s.buildDiff(ctx, entry, members)
	if err != nil {
		return store.Group{}, err
	}
	diff.Invite = inviteChange(entry, state, next)
	// The author, and an added subject, must still hold these keys at
	// commit.
	guard := store.AppendGuard{Accounts: append([]store.MemberView{{AccountID: caller.ID, IdentitySignPub: entry.Author}}, diff.Add...)}

	// E2EE §6: "An invite is consumed when the add entry referencing it is
	// appended." Consumption happens in the append's own transaction; a
	// proof matching no usable invite for this subject rejects the whole
	// append.
	if entry.Type == TypeAdd && len(entry.InviteProof) > 0 {
		guard.InviteMAC, guard.SubjectAccountID = entry.InviteProof, diff.Add[0].AccountID
	}

	updated, err := s.groups.AppendEntry(ctx, logEntry(groupID, entry), entry.Hash(), next.Epoch, diff, guard)
	if errors.Is(err, store.ErrInviteNotUsable) {
		return store.Group{}, ErrInviteProof
	}
	if err != nil {
		return store.Group{}, err
	}

	// Post-commit side effects: best-effort, the committed log is the truth
	// and clients rebuild from it on connect if these don't happen.
	if s.notifier != nil {
		for _, accountID := range diff.Remove {
			s.notifier.CloseAccountConnections(ctx, accountID, string(entry.Type))
		}
		s.notifyMembers(ctx, updated)
	}

	return updated, nil
}

// DeleteGroup soft-deletes the list if the caller is still its owner
// (re-checked inside the delete's transaction), then tells whoever
// could access it (Sync §3, `group`: "the client's access changed").
func (s *Service) DeleteGroup(ctx context.Context, groupID, callerAccountID string) error {
	grp, err := s.groups.Get(ctx, groupID)
	if err != nil {
		return err
	}
	// The audience is read before deletion; afterwards the list is gone
	// from every listing.
	audience, _ := s.groups.AccessibleAccountIDs(ctx, groupID)

	if err := s.groups.SoftDelete(ctx, groupID, callerAccountID); err != nil {
		return err
	}
	if s.notifier != nil {
		s.notifier.NotifyGroup(ctx, groupID, grp.HeadSeq, audience)
	}
	return nil
}

// notifyMembers sends a `group` frame to every current member.
func (s *Service) notifyMembers(ctx context.Context, grp store.Group) {
	audience, err := s.groups.AccessibleAccountIDs(ctx, grp.ID)
	if err != nil {
		slog.Warn("load audience for group notification failed", "group", grp.ID, "error", err)
		return
	}
	s.notifier.NotifyGroup(ctx, grp.ID, grp.HeadSeq, audience)
}

// maxFutureSkew bounds how far ahead of the server clock a new entry's ts
// may be. Rule 7 only bounds ts from below, so without this one far-future
// ts would make every later, honestly-stamped entry fail rule 7. Lives
// here, not in Validate: replaying history must not depend on "now".
const maxFutureSkew = 5 * time.Minute

func checkNotFuture(entry Entry) error {
	if entry.Ts.After(time.Now().Add(maxFutureSkew)) {
		return ErrTimestampFuture
	}
	return nil
}

// checkInviteClock is the server's half of invite expiry: the log rules
// compare expires with the entries' own ts, which a client picks. Only the
// server knows "now", so it refuses to open an invite that is already
// expired and to accept a join after expiry. Like checkNotFuture, this is
// not a replay rule.
func checkInviteClock(entry Entry, state State) error {
	now := time.Now()
	switch entry.Type {
	case TypeInvite:
		if !entry.Expires.After(now) {
			return ErrInviteExpiry
		}
	case TypeJoin:
		if now.After(state.Invites[b64(entry.Invite)].Expires) {
			return ErrInviteExpired
		}
	}
	return nil
}

// inviteChange is what entry does to the invite mirror, given the state
// before (state) and after (next) it.
func inviteChange(entry Entry, state, next State) store.InviteChange {
	var c store.InviteChange
	switch entry.Type {
	case TypeInvite:
		c.Open = &store.LogInvite{InvitePub: entry.Invite, ExpiresAt: entry.Expires, Open: true}
	case TypeUninvite:
		c.Close = entry.Invite
	case TypeJoin:
		c.Close, c.Joined = entry.Invite, true
	}
	c.VoidAll = next.Epoch != state.Epoch
	return c
}

func invitesByKey(invites []store.LogInvite) map[string]InviteState {
	out := make(map[string]InviteState, len(invites))
	for _, li := range invites {
		out[b64(li.InvitePub)] = InviteState{Expires: li.ExpiresAt, Open: li.Open}
	}
	return out
}

// maxBlobBytes bounds each opaque blob in an entry. An HPKE envelope
// or a key_chain is under 100 bytes, an invite MAC 32. Server-side only,
// like maxFutureSkew: it bounds storage, it isn't a replay rule.
const maxBlobBytes = 1024

// checkSizes bounds what one entry can store: no more envelopes than
// members after it, and no oversized blob.
func checkSizes(entry Entry, next State) error {
	tooBig := len(entry.Envelopes) > len(next.Members) ||
		len(entry.KeyChain) > maxBlobBytes || len(entry.InviteProof) > maxBlobBytes
	for _, env := range entry.Envelopes {
		tooBig = tooBig || len(env.HPKECiphertext) > maxBlobBytes
	}
	if tooBig {
		return fmt.Errorf("%w: entry exceeds size limits", ErrMalformed)
	}
	return nil
}

func logEntry(groupID string, e Entry) store.LogEntry {
	return store.LogEntry{GroupID: groupID, Seq: e.Seq, Entry: e.Raw, Sig: e.Sig, Hash: e.Hash(), Ts: e.Ts}
}

func (s *Service) loadMembers(ctx context.Context, groupID string) ([]store.MemberView, error) {
	return s.groups.ListMembersWithKeys(ctx, groupID)
}

func rolesByIdentity(members []store.MemberView) map[string]Role {
	out := make(map[string]Role, len(members))
	for _, m := range members {
		out[b64(m.IdentitySignPub)] = Role(m.Role)
	}
	return out
}

func accountIDByIdentity(members []store.MemberView) map[string]string {
	out := make(map[string]string, len(members))
	for _, m := range members {
		out[b64(m.IdentitySignPub)] = m.AccountID
	}
	return out
}

// buildDiff turns a validated entry into the membership-table change it
// causes. Epoch is handled separately (next.Epoch from Validate) since it
// applies to the group row, not the memberships table.
func (s *Service) buildDiff(ctx context.Context, entry Entry, members []store.MemberView) (store.MembershipDiff, error) {
	byIdentity := accountIDByIdentity(members)

	switch entry.Type {
	case TypeAdd, TypeJoin: // a join's subject is its author
		subject, err := s.accounts.GetByIdentitySignPub(ctx, entry.Subject)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return store.MembershipDiff{}, ErrUnknownAccount
			}
			return store.MembershipDiff{}, err
		}
		// The signed subject_agree is what clients will wrap keys to; it
		// must be the subject's registered key, or the add is a mistake
		// (or a substitution) the server refuses to store.
		if !bytes.Equal(subject.IdentityAgreePub, entry.SubjectAgree) {
			return store.MembershipDiff{}, ErrAgreeKeyMismatch
		}
		// Still listed under an earlier identity: that one must be removed
		// first, so the mirror stays what replaying the log gives.
		for _, m := range members {
			if m.AccountID == subject.ID {
				return store.MembershipDiff{}, ErrAlreadyMember
			}
		}
		return store.MembershipDiff{Add: []store.MemberView{{AccountID: subject.ID, IdentitySignPub: entry.Subject, Role: string(RoleMember)}}}, nil

	case TypeRemove:
		accountID, ok := byIdentity[b64(entry.Subject)]
		if !ok {
			return store.MembershipDiff{}, ErrNotMember
		}
		return store.MembershipDiff{Remove: []string{accountID}}, nil

	case TypeLeave:
		accountID, ok := byIdentity[b64(entry.Author)]
		if !ok {
			return store.MembershipDiff{}, ErrNotMember
		}
		return store.MembershipDiff{Remove: []string{accountID}}, nil

	case TypeRotate, TypeInvite, TypeUninvite:
		return store.MembershipDiff{}, nil

	default:
		return store.MembershipDiff{}, fmt.Errorf("%w: unknown entry type %q", ErrMalformed, entry.Type)
	}
}
