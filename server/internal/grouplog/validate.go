package grouplog

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"maps"
	"time"
)

// MaxInviteLifetime bounds how far past its own ts an invite may expire
// (E2EE §6: "Invites expire after at most 7 days").
const MaxInviteLifetime = 7 * 24 * time.Hour

// JoinProofLabel starts the message a link invite's key signs to admit a
// joiner (E2EE §6.1).
const JoinProofLabel = "runer/v1/join"

// JoinProofMessage is what invite_proof on a join entry signs:
// label ‖ 0x00 ‖ list_id (32 raw bytes) ‖ subject ‖ subject_agree.
func JoinProofMessage(listID []byte, subject, subjectAgree []byte) []byte {
	msg := make([]byte, 0, len(JoinProofLabel)+1+len(listID)+len(subject)+len(subjectAgree))
	msg = append(msg, JoinProofLabel...)
	msg = append(msg, 0)
	msg = append(msg, listID...)
	msg = append(msg, subject...)
	return append(msg, subjectAgree...)
}

// InviteState is one link invite as the log left it.
type InviteState struct {
	Expires time.Time
	Open    bool // false once joined, uninvited, or voided by an epoch change
}

// State is the list's current, server-mirrored state: everything Validate
// needs to check the next entry (E2EE §4.2). Exists is false only when
// validating a genesis (create) entry for a list that does not exist yet.
type State struct {
	Exists bool
	// ListID is the hex list id, SHA-256 of the genesis bytes. Every later
	// entry's `group` must equal it.
	ListID   string
	HeadSeq  int64
	HeadHash []byte
	Epoch    int64
	LastTs   time.Time
	// Members maps identity_sign_pub (base64url) -> Role, the list's
	// current mirrored membership. Exactly one member is RoleOwner.
	Members map[string]Role
	// Invites maps every link-invite key (base64url) ever opened in this
	// list to its state. Closed ones stay, so a key is never reused.
	Invites map[string]InviteState
}

// Validate checks entry against every validity rule in E2EE §4.2
// and, on success, returns the resulting State — entry applied, ready to
// validate the NEXT entry against. A client's validator must reach the
// same verdict on every input in protocol/log-fixtures.
func Validate(state State, entry Entry) (State, error) {
	// Rule 1: seq/prev extend the current head.
	if !state.Exists {
		if entry.Seq != 0 || len(entry.Prev) != 0 {
			return State{}, fmt.Errorf("%w: first entry must be seq 0 with no prev", ErrHeadMismatch)
		}
	} else {
		if entry.Seq != state.HeadSeq+1 || !bytes.Equal(entry.Prev, state.HeadHash) {
			return State{}, ErrHeadMismatch
		}
		if entry.Group != state.ListID {
			return State{}, fmt.Errorf("%w: group is not this list's id", ErrMalformed)
		}
	}

	// Rule 2: create is only valid as seq 0, and seq 0 must be create.
	if entry.Type == TypeCreate {
		if entry.Seq != 0 {
			return State{}, fmt.Errorf("%w: create must be seq 0", ErrMalformed)
		}
	} else if !state.Exists {
		return State{}, fmt.Errorf("%w: first entry of a list must be create", ErrMalformed)
	}

	// Rule 7: ts must be within 24h of the previous entry's ts, or later.
	if state.Exists && entry.Ts.Before(state.LastTs.Add(-24*time.Hour)) {
		return State{}, ErrTimestampSkew
	}

	authorKey := b64(entry.Author)
	remaining := maps.Clone(state.Members)
	invites := maps.Clone(state.Invites)
	if invites == nil {
		invites = map[string]InviteState{}
	}
	newEpoch := state.Epoch
	listID := state.ListID

	switch entry.Type {
	case TypeCreate:
		if entry.Epoch != 0 {
			return State{}, fmt.Errorf("%w: genesis epoch must be 0", ErrEpochMustHold)
		}
		// Rule 8: at most one envelope, K_0 for the author.
		if len(entry.Envelopes) > 1 || (len(entry.Envelopes) == 1 && !bytes.Equal(entry.Envelopes[0].Recipient, entry.Author)) {
			return State{}, fmt.Errorf("%w: create carries at most one envelope, for its author", ErrMalformed)
		}
		remaining = map[string]Role{authorKey: RoleOwner}
		newEpoch = 0
		listID = hex.EncodeToString(entry.Hash())

	case TypeAdd:
		// Rule 3: only the owner adds and removes.
		if state.Members[authorKey] != RoleOwner {
			return State{}, ErrNotOwner
		}
		subjectKey := b64(entry.Subject)
		if _, exists := remaining[subjectKey]; exists {
			return State{}, ErrAlreadyMember
		}
		// Rule 5: epoch does not change on add; one envelope wraps the
		// CURRENT epoch key for the new member.
		if entry.Epoch != state.Epoch {
			return State{}, ErrEpochMustHold
		}
		if !entry.EnvelopeRecipients()[subjectKey] {
			return State{}, ErrMissingEnvelope
		}
		remaining[subjectKey] = RoleMember

	case TypeRemove:
		if state.Members[authorKey] != RoleOwner {
			return State{}, ErrNotOwner
		}
		subjectKey := b64(entry.Subject)
		switch remaining[subjectKey] {
		case "":
			return State{}, ErrNotMember
		case RoleOwner: // Rule 6: the owner deletes the list instead.
			return State{}, ErrOwnerStays
		}
		// Rule 4: removal and rotation are atomic — epoch increases by
		// exactly one, with envelopes for every remaining member.
		if entry.Epoch != state.Epoch+1 {
			return State{}, ErrEpochMustIncrease
		}
		if len(entry.KeyChain) == 0 {
			return State{}, ErrMissingKeyChain
		}
		delete(remaining, subjectKey)
		if err := requireEnvelopeForEach(entry, remaining); err != nil {
			return State{}, err
		}
		newEpoch = state.Epoch + 1

	case TypeLeave:
		if entry.Subject == nil || !bytes.Equal(entry.Author, entry.Subject) {
			return State{}, ErrLeaveNotSelf
		}
		switch remaining[authorKey] {
		case "":
			return State{}, ErrNotMember
		case RoleOwner: // Rule 6.
			return State{}, ErrOwnerStays
		}
		if entry.Epoch != state.Epoch {
			return State{}, ErrEpochMustHold
		}
		delete(remaining, authorKey)

	case TypeRotate:
		// Any current member may rotate (rule 4 applies as for remove).
		if _, isMember := state.Members[authorKey]; !isMember {
			return State{}, ErrNotMember
		}
		if entry.Epoch != state.Epoch+1 {
			return State{}, ErrEpochMustIncrease
		}
		if len(entry.KeyChain) == 0 {
			return State{}, ErrMissingKeyChain
		}
		if err := requireEnvelopeForEach(entry, remaining); err != nil {
			return State{}, err
		}
		newEpoch = state.Epoch + 1

	case TypeInvite:
		// Rule 9: the owner opens a link invite with a fresh key, in the
		// current epoch, expiring within 7 days of its own ts.
		if state.Members[authorKey] != RoleOwner {
			return State{}, ErrNotOwner
		}
		if entry.Epoch != state.Epoch {
			return State{}, ErrEpochMustHold
		}
		inviteKey := b64(entry.Invite)
		if _, seen := invites[inviteKey]; seen {
			return State{}, ErrInviteReused
		}
		if !entry.Expires.After(entry.Ts) || entry.Expires.After(entry.Ts.Add(MaxInviteLifetime)) {
			return State{}, ErrInviteExpiry
		}
		invites[inviteKey] = InviteState{Expires: entry.Expires, Open: true}

	case TypeUninvite:
		// Rule 10: the owner closes an open invite.
		if state.Members[authorKey] != RoleOwner {
			return State{}, ErrNotOwner
		}
		if entry.Epoch != state.Epoch {
			return State{}, ErrEpochMustHold
		}
		inviteKey := b64(entry.Invite)
		if !invites[inviteKey].Open {
			return State{}, ErrInviteNotOpen
		}
		invites[inviteKey] = InviteState{Expires: invites[inviteKey].Expires}

	case TypeJoin:
		// Rule 11: whoever holds an open invite's key admits themselves,
		// once, before it expires, in the epoch it was opened in.
		if _, exists := remaining[authorKey]; exists {
			return State{}, ErrAlreadyMember
		}
		if entry.Epoch != state.Epoch {
			return State{}, ErrEpochMustHold
		}
		inviteKey := b64(entry.Invite)
		inv := invites[inviteKey]
		if !inv.Open {
			return State{}, ErrInviteNotOpen
		}
		if entry.Ts.After(inv.Expires) {
			return State{}, ErrInviteExpired
		}
		rawListID, err := hex.DecodeString(state.ListID)
		if err != nil {
			return State{}, fmt.Errorf("%w: list id is not hex", ErrMalformed)
		}
		if !ed25519.Verify(entry.Invite, JoinProofMessage(rawListID, entry.Subject, entry.SubjectAgree), entry.InviteProof) {
			return State{}, ErrJoinProof
		}
		invites[inviteKey] = InviteState{Expires: inv.Expires}
		remaining[authorKey] = RoleMember

	default:
		return State{}, fmt.Errorf("%w: unknown entry type %q", ErrMalformed, entry.Type)
	}

	// Rule 12: a new epoch voids every open invite; its package carries
	// the old key.
	if newEpoch != state.Epoch {
		for k, inv := range invites {
			invites[k] = InviteState{Expires: inv.Expires}
		}
	}

	return State{
		Exists: true, ListID: listID,
		HeadSeq: entry.Seq, HeadHash: entry.Hash(), Epoch: newEpoch, LastTs: entry.Ts,
		Members: remaining, Invites: invites,
	}, nil
}

// requireEnvelopeForEach checks entry carries an envelope for every member
// in `remaining` (rule 4: "carry envelopes for every remaining member").
func requireEnvelopeForEach(entry Entry, remaining map[string]Role) error {
	recipients := entry.EnvelopeRecipients()
	for member := range remaining {
		if !recipients[member] {
			return ErrMissingEnvelope
		}
	}
	return nil
}
