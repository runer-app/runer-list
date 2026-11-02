package grouplog

import (
	"errors"
	"fmt"
)

// Category sentinels: internal/server's central error handler classifies
// any grouplog error into an HTTP status via errors.Is against just these
// three, rather than enumerating every specific rule violation.
var (
	ErrConflict  = errors.New("conflict")        // -> 409: caller's view of the head/state is stale
	ErrForbidden = errors.New("forbidden")       // -> 403: authorization rule violated
	ErrMalformed = errors.New("malformed entry") // -> 400: bad request shape or content
)

// Specific errors, named after the validity rule they enforce (E2EE
// Protocol §4.2, rules 1-12), each wrapping the category it maps to.
var (
	ErrInvalidSignature = fmt.Errorf("%w: invalid signature", ErrMalformed)

	// ErrHeadMismatch is rule 1: seq/prev must extend the current head,
	// matching "409 if prev does not match the head" (API Reference).
	ErrHeadMismatch = fmt.Errorf("%w: entry does not extend the current head", ErrConflict)

	ErrNotOwner          = fmt.Errorf("%w: author is not the list owner", ErrForbidden)                         // rule 3
	ErrNotMember         = fmt.Errorf("%w: author or subject is not a current member", ErrForbidden)            // rules 3, 4
	ErrLeaveNotSelf      = fmt.Errorf("%w: leave author must be the subject", ErrForbidden)                     // rule 6
	ErrEpochMustIncrease = fmt.Errorf("%w: remove/rotate must increase the epoch by exactly one", ErrMalformed) // rule 4
	ErrEpochMustHold     = fmt.Errorf("%w: create/add/leave must not change the epoch", ErrMalformed)           // rule 5
	ErrMissingEnvelope   = fmt.Errorf("%w: missing envelope for a remaining member", ErrMalformed)              // rules 4, 5
	ErrMissingKeyChain   = fmt.Errorf("%w: remove/rotate must carry key_chain", ErrMalformed)
	ErrOwnerStays        = fmt.Errorf("%w: the owner cannot leave or be removed; delete the list instead", ErrForbidden) // rule 6
	ErrTimestampSkew     = fmt.Errorf("%w: ts is more than 24h before the previous entry", ErrMalformed)                 // rule 7
	ErrTimestampFuture   = fmt.Errorf("%w: ts is too far in the future", ErrMalformed)                                   // server-side only, not a replay rule
	ErrUnknownAccount    = fmt.Errorf("%w: author or subject is not a registered account", ErrMalformed)
	ErrAlreadyMember     = fmt.Errorf("%w: subject is already a member", ErrConflict)
	ErrAuthorNotCaller   = fmt.Errorf("%w: entry author is not the caller's identity", ErrForbidden)
	ErrInviteProof       = fmt.Errorf("%w: invite_proof matches no usable invite for this subject", ErrForbidden)
	ErrAgreeKeyMismatch  = fmt.Errorf("%w: agreement key is not the account's registered key", ErrMalformed)
	ErrInboxPrivate      = fmt.Errorf("%w: an Inbox can't be shared", ErrForbidden)

	ErrInviteReused  = fmt.Errorf("%w: invite key was already used in this list", ErrMalformed)          // rule 9
	ErrInviteExpiry  = fmt.Errorf("%w: expires must be after ts and at most 7 days later", ErrMalformed) // rule 9
	ErrInviteNotOpen = fmt.Errorf("%w: invite is not open (joined, uninvited or voided)", ErrForbidden)  // rules 10, 11
	ErrInviteExpired = fmt.Errorf("%w: invite has expired", ErrForbidden)                                // rule 11
	ErrJoinProof     = fmt.Errorf("%w: invite_proof is not the invite key's signature", ErrForbidden)    // rule 11
)
