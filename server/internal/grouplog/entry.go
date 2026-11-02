// Package grouplog implements the signed, hash-chained membership log:
// entry parsing, the validity rules (E2EE §4.2, identical in
// intent to what client-core verifies), and compare-and-swap append.
package grouplog

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

type EntryType string

const (
	TypeCreate EntryType = "create"
	TypeAdd    EntryType = "add"
	TypeRemove EntryType = "remove"
	TypeLeave  EntryType = "leave"
	TypeRotate EntryType = "rotate"
	// Link invites (E2EE §6): the owner opens one with `invite`, the
	// invitee admits themselves with `join`, the owner cancels with
	// `uninvite`.
	TypeInvite   EntryType = "invite"
	TypeUninvite EntryType = "uninvite"
	TypeJoin     EntryType = "join"
)

// Role is not on the wire: the genesis author is the list's owner, everyone
// added later is a member.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

// Envelope is a parsed HPKE envelope (E2EE §5): opaque ciphertext wrapping
// the group key for one recipient. The server never opens it, only checks
// that the right recipients are covered.
type Envelope struct {
	Recipient      []byte
	HPKECiphertext []byte
}

// Entry is a parsed, signature-verified membership log entry, plus the
// exact raw bytes it was signed over. Every validity check and every hash
// (including the next entry's `prev` and a genesis entry's group id) is
// computed from Raw — never from a re-serialization of the parsed fields
// (E2EE §4.1: "Entries are stored as bytes and never re-serialized before
// verifying").
type Entry struct {
	Raw []byte
	Sig []byte

	V       int
	Group   string
	Seq     int64
	Prev    []byte
	Type    EntryType
	Author  ed25519.PublicKey
	Subject ed25519.PublicKey
	// AuthorAgree (create) and SubjectAgree (add) are the X25519 keys the
	// group key is wrapped to. Signed next to the signing key, so clients
	// never take an encryption key from the server.
	AuthorAgree  []byte
	SubjectAgree []byte
	Epoch        int64
	Envelopes    []Envelope
	KeyChain     []byte
	// Invite is a link invite's one-time Ed25519 public key (invite,
	// uninvite, join); Expires bounds when it can be joined (invite only).
	Invite      ed25519.PublicKey
	Expires     time.Time
	InviteProof []byte
	Ts          time.Time
}

// Hash is this entry's identity as referenced by the next entry's `prev`,
// and — for a genesis entry — the group id itself.
func (e Entry) Hash() []byte {
	h := sha256.Sum256(e.Raw)
	return h[:]
}

// wireEntry is the one canonical encoding of an entry (E2EE §4.1): these
// keys, in this order, lowercase, no whitespace, optional fields omitted
// when empty, integers in [0, 2^53-1], binary fields base64url without
// padding, ts in Unix milliseconds. Parse accepts only bytes that are
// exactly this encoding of what they decode to, so every parser, the
// server's or a client's, reads the same entry the same way.
type wireEntry struct {
	V            int            `json:"v"`
	Group        string         `json:"group,omitempty"`
	Seq          int64          `json:"seq"`
	Prev         string         `json:"prev,omitempty"`
	Type         EntryType      `json:"type"`
	Author       string         `json:"author"`
	AuthorAgree  string         `json:"author_agree,omitempty"`
	Subject      string         `json:"subject,omitempty"`
	SubjectAgree string         `json:"subject_agree,omitempty"`
	Epoch        int64          `json:"epoch"`
	Envelopes    []wireEnvelope `json:"envelopes,omitempty"`
	KeyChain     string         `json:"key_chain,omitempty"`
	Invite       string         `json:"invite,omitempty"`
	Expires      int64          `json:"expires,omitempty"`
	InviteProof  string         `json:"invite_proof,omitempty"`
	Ts           int64          `json:"ts"`
}

type wireEnvelope struct {
	Recipient      string `json:"recipient"`
	HPKECiphertext string `json:"hpke_ciphertext"`
}

// MaxSafeInt is the largest integer every client language represents
// exactly (JavaScript's Number.MAX_SAFE_INTEGER).
const MaxSafeInt = 1<<53 - 1

// Parse decodes and signature-verifies a submitted entry, and checks it is
// in canonical form with the fields its type requires. It does not run the
// group-state-dependent validity rules (1-12) — see Validate.
func Parse(raw, sig []byte) (Entry, error) {
	var w wireEntry
	if err := json.Unmarshal(raw, &w); err != nil {
		return Entry{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if err := requireCanonical(raw, w); err != nil {
		return Entry{}, err
	}
	if w.V != 1 {
		return Entry{}, fmt.Errorf("%w: unsupported version %d", ErrMalformed, w.V)
	}
	for _, n := range []int64{w.Seq, w.Epoch, w.Expires, w.Ts} {
		if n < 0 || n > MaxSafeInt {
			return Entry{}, fmt.Errorf("%w: integer out of range", ErrMalformed)
		}
	}
	if err := checkShape(w); err != nil {
		return Entry{}, err
	}

	author, err := decodeFixed(w.Author, ed25519.PublicKeySize)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: author: %v", ErrMalformed, err)
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(author, raw, sig) {
		return Entry{}, ErrInvalidSignature
	}

	e := Entry{
		Raw: raw, Sig: sig,
		V: w.V, Group: w.Group, Seq: w.Seq,
		Type: w.Type, Author: author, Epoch: w.Epoch,
		Ts: time.UnixMilli(w.Ts).UTC(),
	}

	if w.AuthorAgree != "" {
		if e.AuthorAgree, err = decodeFixed(w.AuthorAgree, X25519KeySize); err != nil {
			return Entry{}, fmt.Errorf("%w: author_agree: %v", ErrMalformed, err)
		}
	}
	if w.SubjectAgree != "" {
		if e.SubjectAgree, err = decodeFixed(w.SubjectAgree, X25519KeySize); err != nil {
			return Entry{}, fmt.Errorf("%w: subject_agree: %v", ErrMalformed, err)
		}
	}
	if w.Prev != "" {
		if e.Prev, err = decodeAny(w.Prev); err != nil {
			return Entry{}, fmt.Errorf("%w: prev: %v", ErrMalformed, err)
		}
	}
	if w.Subject != "" {
		if e.Subject, err = decodeFixed(w.Subject, ed25519.PublicKeySize); err != nil {
			return Entry{}, fmt.Errorf("%w: subject: %v", ErrMalformed, err)
		}
	}
	if w.KeyChain != "" {
		if e.KeyChain, err = decodeAny(w.KeyChain); err != nil {
			return Entry{}, fmt.Errorf("%w: key_chain: %v", ErrMalformed, err)
		}
	}
	if w.Invite != "" {
		if e.Invite, err = decodeFixed(w.Invite, ed25519.PublicKeySize); err != nil {
			return Entry{}, fmt.Errorf("%w: invite: %v", ErrMalformed, err)
		}
	}
	if w.Expires != 0 {
		e.Expires = time.UnixMilli(w.Expires).UTC()
	}
	if w.InviteProof != "" {
		if e.InviteProof, err = decodeAny(w.InviteProof); err != nil {
			return Entry{}, fmt.Errorf("%w: invite_proof: %v", ErrMalformed, err)
		}
	}
	for _, we := range w.Envelopes {
		recipient, err := decodeFixed(we.Recipient, ed25519.PublicKeySize)
		if err != nil {
			return Entry{}, fmt.Errorf("%w: envelope recipient: %v", ErrMalformed, err)
		}
		ct, err := decodeAny(we.HPKECiphertext)
		if err != nil {
			return Entry{}, fmt.Errorf("%w: envelope ciphertext: %v", ErrMalformed, err)
		}
		e.Envelopes = append(e.Envelopes, Envelope{Recipient: recipient, HPKECiphertext: ct})
	}

	return e, nil
}

// X25519KeySize is the length of a raw X25519 public key.
const X25519KeySize = 32

// requireCanonical rejects raw unless it is byte-for-byte the canonical
// encoding of w. This one comparison rejects case-variant and duplicate
// keys (which encoding/json would silently fold or overwrite), unknown
// fields, whitespace, key reordering, and alternative number spellings.
func requireCanonical(raw []byte, w any) error {
	canonical, err := CanonicalJSON(w)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if !bytes.Equal(raw, canonical) {
		return fmt.Errorf("%w: not in canonical form", ErrMalformed)
	}
	return nil
}

// CanonicalJSON encodes v compactly, without HTML escaping.
func CanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// checkShape enforces which fields each entry type carries (E2EE §4.2,
// rule 8): author_agree on create only, no group on create, subject_agree
// on add and join only, and the link-invite fields only where they mean
// something.
func checkShape(w wireEntry) error {
	if w.Type == TypeCreate {
		switch {
		case w.Group != "":
			return fmt.Errorf("%w: create has no group; its id is its hash", ErrMalformed)
		case w.AuthorAgree == "":
			return fmt.Errorf("%w: create needs author_agree", ErrMalformed)
		}
	} else if w.AuthorAgree != "" {
		return fmt.Errorf("%w: author_agree belongs to create only", ErrMalformed)
	}
	admits := w.Type == TypeAdd || w.Type == TypeJoin
	if admits != (w.SubjectAgree != "") {
		return fmt.Errorf("%w: subject_agree is required on add and join and only there", ErrMalformed)
	}

	linkInvite := w.Type == TypeInvite || w.Type == TypeUninvite || w.Type == TypeJoin
	switch {
	case linkInvite != (w.Invite != ""):
		return fmt.Errorf("%w: invite is required on invite, uninvite and join and only there", ErrMalformed)
	case (w.Type == TypeInvite) != (w.Expires != 0):
		return fmt.Errorf("%w: expires is required on invite and only there", ErrMalformed)
	case w.InviteProof != "" && w.Type != TypeAdd && w.Type != TypeJoin:
		return fmt.Errorf("%w: invite_proof belongs to add and join only", ErrMalformed)
	}
	if !linkInvite {
		return nil
	}
	if len(w.Envelopes) > 0 || w.KeyChain != "" {
		return fmt.Errorf("%w: %s carries no envelopes or key_chain", ErrMalformed, w.Type)
	}
	if w.Type == TypeJoin {
		if w.Subject == "" || w.Subject != w.Author {
			return fmt.Errorf("%w: join author must be the subject", ErrMalformed)
		}
		if w.InviteProof == "" {
			return fmt.Errorf("%w: join needs invite_proof", ErrMalformed)
		}
	} else if w.Subject != "" {
		return fmt.Errorf("%w: %s has no subject", ErrMalformed, w.Type)
	}
	return nil
}

func decodeAny(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

func decodeFixed(s string, wantLen int) ([]byte, error) {
	b, err := decodeAny(s)
	if err != nil {
		return nil, err
	}
	if len(b) != wantLen {
		return nil, fmt.Errorf("want %d bytes, got %d", wantLen, len(b))
	}
	return b, nil
}

// EnvelopeRecipients returns the set of recipient keys covered by e's
// envelopes, base64url-encoded.
func (e Entry) EnvelopeRecipients() map[string]bool {
	out := make(map[string]bool, len(e.Envelopes))
	for _, env := range e.Envelopes {
		out[base64.RawURLEncoding.EncodeToString(env.Recipient)] = true
	}
	return out
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
