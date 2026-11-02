package grouplog

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	return pub
}

// Rule 6: the owner can neither leave nor be removed; everyone else can
// leave, and only the owner removes.
func TestValidate_OwnerStays(t *testing.T) {
	alice, bob := testKey(t), testKey(t)
	now := time.Now()
	state := State{
		Exists: true, HeadSeq: 1, HeadHash: []byte("head"), LastTs: now,
		Members: map[string]Role{b64(alice): RoleOwner, b64(bob): RoleMember},
	}

	_, err := Validate(state, Entry{Seq: 2, Prev: []byte("head"), Type: TypeLeave, Author: alice, Subject: alice, Ts: now})
	require.ErrorIs(t, err, ErrOwnerStays)

	_, err = Validate(state, Entry{
		Seq: 2, Prev: []byte("head"), Type: TypeRemove, Author: bob, Subject: alice, Epoch: 1,
		Envelopes: []Envelope{{Recipient: bob}}, KeyChain: []byte("kc"), Ts: now,
	})
	require.ErrorIs(t, err, ErrNotOwner)

	next, err := Validate(state, Entry{Seq: 2, Prev: []byte("head"), Type: TypeLeave, Author: bob, Subject: bob, Ts: now})
	require.NoError(t, err)
	require.Equal(t, map[string]Role{b64(alice): RoleOwner}, next.Members)
}

// Rule 8: create carries at most one envelope, K_0 for its author.
func TestValidate_CreateEnvelopes(t *testing.T) {
	alice, bob := testKey(t), testKey(t)
	genesis := func(recipients ...ed25519.PublicKey) Entry {
		e := Entry{Raw: []byte("genesis"), Type: TypeCreate, Author: alice, Ts: time.Now()}
		for _, r := range recipients {
			e.Envelopes = append(e.Envelopes, Envelope{Recipient: r})
		}
		return e
	}

	for _, ok := range [][]ed25519.PublicKey{nil, {alice}} {
		_, err := Validate(State{}, genesis(ok...))
		require.NoError(t, err)
	}
	for _, bad := range [][]ed25519.PublicKey{{bob}, {alice, alice}, {alice, bob}} {
		_, err := Validate(State{}, genesis(bad...))
		require.ErrorIs(t, err, ErrMalformed)
	}
}
