package grouplog

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGenerateFixtures (re)writes protocol/log-fixtures/*.json: the shared
// valid/invalid membership-log test vectors this validator and a
// client's must agree on (E2EE §4.3).
// Skipped by default — run with GENERATE_FIXTURES=1 to regenerate after a
// deliberate format change. The checked-in files are the actual test
// input; this generator exists so they're reproducible, not because tests
// run it.
func TestGenerateFixtures(t *testing.T) {
	if os.Getenv("GENERATE_FIXTURES") == "" {
		t.Skip("set GENERATE_FIXTURES=1 to regenerate protocol/log-fixtures")
	}

	dir := fixturesDir(t)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	alicePub, alicePriv := fixtureKey("alice")
	bobPub, bobPriv := fixtureKey("bob")
	carolPub, _ := fixtureKey("carol")

	baseTs := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// --- Valid: create -> add Bob -> remove Bob (full lifecycle). ---
	{
		baseTs := baseTs.Add(0 * 24 * time.Hour)
		genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.UnixMilli()}
		genesisRaw, genesisSig := signEntry(t, alicePriv, genesis)
		groupID := groupIDFromGenesis(genesisRaw)

		aliceEnvOnly := []wireEnvelope{{Recipient: b64(alicePub), HPKECiphertext: b64([]byte("ct-for-alice"))}}
		add := wireEntry{V: 1, Group: groupID, Seq: 1, Prev: hashB64(genesisRaw), Type: TypeAdd,
			Author: b64(alicePub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"), Epoch: 0,
			Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}},
			Ts:        baseTs.Add(time.Minute).UnixMilli()}
		addRaw, addSig := signEntry(t, alicePriv, add)

		remove := wireEntry{V: 1, Group: groupID, Seq: 2, Prev: hashB64(addRaw), Type: TypeRemove,
			Author: b64(alicePub), Subject: b64(bobPub), Epoch: 1,
			Envelopes: aliceEnvOnly, KeyChain: b64([]byte("key-chain-0-to-1")),
			Ts: baseTs.Add(2 * time.Minute).UnixMilli()}
		removeRaw, removeSig := signEntry(t, alicePriv, remove)

		writeFixture(t, dir, fixture{
			Name: "list_lifecycle_valid", Valid: true,
			Entries: []fixtureEntry{
				{Raw: genesisRaw, Sig: genesisSig},
				{Raw: addRaw, Sig: addSig},
				{Raw: removeRaw, Sig: removeSig},
			},
		})
	}

	// --- Invalid: bad_signature — last entry's signature doesn't match. ---
	{
		baseTs := baseTs.Add(1 * 24 * time.Hour)
		genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.UnixMilli()}
		genesisRaw, genesisSig := signEntry(t, alicePriv, genesis)
		groupID := groupIDFromGenesis(genesisRaw)

		add := wireEntry{V: 1, Group: groupID, Seq: 1, Prev: hashB64(genesisRaw), Type: TypeAdd,
			Author: b64(alicePub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"), Epoch: 0,
			Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}},
			Ts:        baseTs.Add(time.Minute).UnixMilli()}
		addRaw, _ := signEntry(t, alicePriv, add)
		_, wrongSig := signEntry(t, bobPriv, add) // signed by the wrong key

		writeFixture(t, dir, fixture{
			Name: "bad_signature", Valid: false,
			Entries: []fixtureEntry{
				{Raw: genesisRaw, Sig: genesisSig},
				{Raw: addRaw, Sig: wrongSig},
			},
		})
	}

	// --- Invalid: wrong_prev — last entry's prev doesn't hash-chain. ---
	{
		baseTs := baseTs.Add(2 * 24 * time.Hour)
		genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.UnixMilli()}
		genesisRaw, genesisSig := signEntry(t, alicePriv, genesis)
		groupID := groupIDFromGenesis(genesisRaw)

		add := wireEntry{V: 1, Group: groupID, Seq: 1, Prev: hashB64([]byte("not-the-real-genesis-bytes")), Type: TypeAdd,
			Author: b64(alicePub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"), Epoch: 0,
			Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}},
			Ts:        baseTs.Add(time.Minute).UnixMilli()}
		addRaw, addSig := signEntry(t, alicePriv, add)

		writeFixture(t, dir, fixture{
			Name: "wrong_prev", Valid: false,
			Entries: []fixtureEntry{
				{Raw: genesisRaw, Sig: genesisSig},
				{Raw: addRaw, Sig: addSig},
			},
		})
	}

	// --- Invalid: non_owner_add — Carol (a plain member) tries to add someone. ---
	{
		baseTs := baseTs.Add(3 * 24 * time.Hour)
		genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.UnixMilli()}
		genesisRaw, genesisSig := signEntry(t, alicePriv, genesis)
		groupID := groupIDFromGenesis(genesisRaw)

		addCarol := wireEntry{V: 1, Group: groupID, Seq: 1, Prev: hashB64(genesisRaw), Type: TypeAdd,
			Author: b64(alicePub), Subject: b64(carolPub), SubjectAgree: agreeKey("carol"), Epoch: 0,
			Envelopes: []wireEnvelope{{Recipient: b64(carolPub), HPKECiphertext: b64([]byte("ct-for-carol"))}},
			Ts:        baseTs.Add(time.Minute).UnixMilli()}
		addCarolRaw, addCarolSig := signEntry(t, alicePriv, addCarol)

		_, carolPriv := fixtureKey("carol")
		addBobByCarol := wireEntry{V: 1, Group: groupID, Seq: 2, Prev: hashB64(addCarolRaw), Type: TypeAdd,
			Author: b64(carolPub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"), Epoch: 0,
			Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}},
			Ts:        baseTs.Add(2 * time.Minute).UnixMilli()}
		addBobRaw, addBobSig := signEntry(t, carolPriv, addBobByCarol)

		writeFixture(t, dir, fixture{
			Name: "non_owner_add", Valid: false,
			Entries: []fixtureEntry{
				{Raw: genesisRaw, Sig: genesisSig},
				{Raw: addCarolRaw, Sig: addCarolSig},
				{Raw: addBobRaw, Sig: addBobSig},
			},
		})
	}

	// --- Invalid: remove_without_envelopes — remove omits the remaining member's envelope. ---
	{
		baseTs := baseTs.Add(4 * 24 * time.Hour)
		genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.UnixMilli()}
		genesisRaw, genesisSig := signEntry(t, alicePriv, genesis)
		groupID := groupIDFromGenesis(genesisRaw)

		add := wireEntry{V: 1, Group: groupID, Seq: 1, Prev: hashB64(genesisRaw), Type: TypeAdd,
			Author: b64(alicePub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"), Epoch: 0,
			Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}},
			Ts:        baseTs.Add(time.Minute).UnixMilli()}
		addRaw, addSig := signEntry(t, alicePriv, add)

		// Removing Bob: Alice remains, but no envelope for Alice under the new epoch.
		remove := wireEntry{V: 1, Group: groupID, Seq: 2, Prev: hashB64(addRaw), Type: TypeRemove,
			Author: b64(alicePub), Subject: b64(bobPub), Epoch: 1,
			KeyChain: b64([]byte("key-chain-0-to-1")),
			Ts:       baseTs.Add(2 * time.Minute).UnixMilli()}
		removeRaw, removeSig := signEntry(t, alicePriv, remove)

		writeFixture(t, dir, fixture{
			Name: "remove_without_envelopes", Valid: false,
			Entries: []fixtureEntry{
				{Raw: genesisRaw, Sig: genesisSig},
				{Raw: addRaw, Sig: addSig},
				{Raw: removeRaw, Sig: removeSig},
			},
		})
	}

	// --- Invalid: owner_removed — the owner cannot be removed (rule 6). ---
	{
		baseTs := baseTs.Add(5 * 24 * time.Hour)
		genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.UnixMilli()}
		genesisRaw, genesisSig := signEntry(t, alicePriv, genesis)
		groupID := groupIDFromGenesis(genesisRaw)

		add := wireEntry{V: 1, Group: groupID, Seq: 1, Prev: hashB64(genesisRaw), Type: TypeAdd,
			Author: b64(alicePub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"), Epoch: 0,
			Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}},
			Ts:        baseTs.Add(time.Minute).UnixMilli()}
		addRaw, addSig := signEntry(t, alicePriv, add)

		// Alice (the owner) tries to remove herself.
		remove := wireEntry{V: 1, Group: groupID, Seq: 2, Prev: hashB64(addRaw), Type: TypeRemove,
			Author: b64(alicePub), Subject: b64(alicePub), Epoch: 1,
			Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}},
			KeyChain:  b64([]byte("key-chain-0-to-1")),
			Ts:        baseTs.Add(2 * time.Minute).UnixMilli()}
		removeRaw, removeSig := signEntry(t, alicePriv, remove)

		writeFixture(t, dir, fixture{
			Name: "owner_removed", Valid: false,
			Entries: []fixtureEntry{
				{Raw: genesisRaw, Sig: genesisSig},
				{Raw: addRaw, Sig: addSig},
				{Raw: removeRaw, Sig: removeSig},
			},
		})
	}

	// --- Invalid: create_at_seq_nonzero — a lone genesis entry at seq 1. ---
	{
		baseTs := baseTs.Add(6 * 24 * time.Hour)
		genesis := wireEntry{V: 1, Seq: 1, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.UnixMilli()}
		genesisRaw, genesisSig := signEntry(t, alicePriv, genesis)

		writeFixture(t, dir, fixture{
			Name: "create_at_seq_nonzero", Valid: false,
			Entries: []fixtureEntry{
				{Raw: genesisRaw, Sig: genesisSig},
			},
		})
	}

	// --- Invalid: bad_ts — add entry timestamped more than 24h before genesis. ---
	{
		baseTs := baseTs.Add(7 * 24 * time.Hour)
		genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.UnixMilli()}
		genesisRaw, genesisSig := signEntry(t, alicePriv, genesis)
		groupID := groupIDFromGenesis(genesisRaw)

		add := wireEntry{V: 1, Group: groupID, Seq: 1, Prev: hashB64(genesisRaw), Type: TypeAdd,
			Author: b64(alicePub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"), Epoch: 0,
			Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}},
			Ts:        baseTs.Add(-48 * time.Hour).UnixMilli()}
		addRaw, addSig := signEntry(t, alicePriv, add)

		writeFixture(t, dir, fixture{
			Name: "bad_ts", Valid: false,
			Entries: []fixtureEntry{
				{Raw: genesisRaw, Sig: genesisSig},
				{Raw: addRaw, Sig: addSig},
			},
		})
	}

	// --- Invalid: non-canonical encodings and bad field values. Each
	// fixture is a genesis plus a correctly signed add whose bytes aren't
	// the one canonical form (or break a field rule); a validator must
	// reject it rather than pick one of several readings. Each fixture gets
	// its own genesis (own group id), so they can all be replayed against
	// one server. ---
	mutations := []struct {
		name   string
		mutate func(add wireEntry, canonical string) string // returns the raw bytes to sign
	}{
		{"noncanonical_case_variant_key", func(_ wireEntry, c string) string { return strings.Replace(c, `"epoch":0`, `"epoch":0,"Epoch":1`, 1) }},
		{"noncanonical_duplicate_key", func(_ wireEntry, c string) string { return strings.Replace(c, `"epoch":0`, `"epoch":0,"epoch":1`, 1) }},
		{"noncanonical_whitespace", func(_ wireEntry, c string) string { return strings.Replace(c, `{"v":1`, `{ "v":1`, 1) }},
		{"noncanonical_unknown_field", func(_ wireEntry, c string) string {
			return strings.Replace(c, `"epoch":0`, `"epoch":0,"extra":true`, 1)
		}},
		{"noncanonical_number_form", func(_ wireEntry, c string) string { return strings.Replace(c, `"epoch":0`, `"epoch":0.0`, 1) }},
		{"noncanonical_key_order", func(_ wireEntry, c string) string {
			return strings.TrimSuffix(strings.Replace(c, `{"v":1,`, `{`, 1), "}") + `,"v":1}`
		}},
		{"integer_out_of_range", func(add wireEntry, _ string) string {
			add.Ts = MaxSafeInt + 1
			raw, _ := CanonicalJSON(add)
			return string(raw)
		}},
		{"add_missing_subject_agree", func(add wireEntry, _ string) string {
			add.SubjectAgree = ""
			raw, _ := CanonicalJSON(add)
			return string(raw)
		}},
	}
	for i, m := range mutations {
		baseTs := baseTs.Add(time.Duration(8+i) * 24 * time.Hour)
		genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.UnixMilli()}
		genesisRaw, genesisSig := signEntry(t, alicePriv, genesis)
		add := wireEntry{V: 1, Group: groupIDFromGenesis(genesisRaw), Seq: 1, Prev: hashB64(genesisRaw), Type: TypeAdd,
			Author: b64(alicePub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"), Epoch: 0,
			Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}},
			Ts:        baseTs.Add(time.Minute).UnixMilli()}
		canonical, _ := signEntry(t, alicePriv, add)
		raw := []byte(m.mutate(add, string(canonical)))
		require.NotEqual(t, canonical, raw, m.name)
		writeFixture(t, dir, fixture{
			Name: m.name, Valid: false,
			Entries: []fixtureEntry{
				{Raw: genesisRaw, Sig: genesisSig},
				{Raw: raw, Sig: ed25519.Sign(alicePriv, raw)},
			},
		})
	}

	// --- Invalid: create_with_group — a genesis has no group field; its
	// hash is the group id. ---
	{
		genesis := wireEntry{V: 1, Group: "x", Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.Add(21 * 24 * time.Hour).UnixMilli()}
		raw, sig := signEntry(t, alicePriv, genesis)
		writeFixture(t, dir, fixture{Name: "create_with_group", Valid: false, Entries: []fixtureEntry{{Raw: raw, Sig: sig}}})
	}

	// --- Invalid: non_owner_remove — Bob (a plain member) tries to remove Carol. ---
	{
		baseTs := baseTs.Add(22 * 24 * time.Hour)
		genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: baseTs.UnixMilli()}
		genesisRaw, genesisSig := signEntry(t, alicePriv, genesis)
		groupID := groupIDFromGenesis(genesisRaw)
		envs := func(keys ...ed25519.PublicKey) []wireEnvelope {
			var out []wireEnvelope
			for _, k := range keys {
				out = append(out, wireEnvelope{Recipient: b64(k), HPKECiphertext: b64([]byte("ct"))})
			}
			return out
		}

		addBob := wireEntry{V: 1, Group: groupID, Seq: 1, Prev: hashB64(genesisRaw), Type: TypeAdd,
			Author: b64(alicePub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"), Epoch: 0,
			Envelopes: envs(bobPub), Ts: baseTs.Add(time.Minute).UnixMilli()}
		addBobRaw, addBobSig := signEntry(t, alicePriv, addBob)
		addCarol := wireEntry{V: 1, Group: groupID, Seq: 2, Prev: hashB64(addBobRaw), Type: TypeAdd,
			Author: b64(alicePub), Subject: b64(carolPub), SubjectAgree: agreeKey("carol"), Epoch: 0,
			Envelopes: envs(carolPub), Ts: baseTs.Add(2 * time.Minute).UnixMilli()}
		addCarolRaw, addCarolSig := signEntry(t, alicePriv, addCarol)
		remove := wireEntry{V: 1, Group: groupID, Seq: 3, Prev: hashB64(addCarolRaw), Type: TypeRemove,
			Author: b64(bobPub), Subject: b64(carolPub), Epoch: 1,
			Envelopes: envs(alicePub, bobPub), KeyChain: b64([]byte("key-chain-0-to-1")),
			Ts: baseTs.Add(3 * time.Minute).UnixMilli()}
		removeRaw, removeSig := signEntry(t, bobPriv, remove)

		writeFixture(t, dir, fixture{Name: "non_owner_remove", Valid: false, Entries: []fixtureEntry{
			{Raw: genesisRaw, Sig: genesisSig},
			{Raw: addBobRaw, Sig: addBobSig},
			{Raw: addCarolRaw, Sig: addCarolSig},
			{Raw: removeRaw, Sig: removeSig},
		}})
	}

	// --- Link invites (E2EE §6, rules 9-12). Each case is its own list. ---
	carolPubKey, carolPriv := fixtureKey("carol")
	linkCases := []struct {
		name  string
		valid bool
		build func(l *linkLog)
	}{
		// create -> invite A -> Bob joins via A -> invite B -> uninvite B -> remove Bob.
		{"link_invite_lifecycle_valid", true, func(l *linkLog) {
			invA, invAPriv := fixtureKey("invite-a")
			invB, _ := fixtureKey("invite-b")
			l.invite(alicePriv, invA, 0, time.Hour)
			l.join(bobPriv, "bob", invA, invAPriv, 0)
			l.invite(alicePriv, invB, 0, time.Hour)
			l.uninvite(alicePriv, invB, 0)
			l.append(alicePriv, wireEntry{Type: TypeRemove, Author: b64(alicePub), Subject: b64(bobPub), Epoch: 1,
				Envelopes: []wireEnvelope{{Recipient: b64(alicePub), HPKECiphertext: b64([]byte("ct-for-alice"))}},
				KeyChain:  b64([]byte("key-chain-0-to-1"))})
		}},
		{"join_after_uninvite", false, func(l *linkLog) {
			inv, invPriv := fixtureKey("invite-a")
			l.invite(alicePriv, inv, 0, time.Hour)
			l.uninvite(alicePriv, inv, 0)
			l.join(bobPriv, "bob", inv, invPriv, 0)
		}},
		{"join_after_epoch_change", false, func(l *linkLog) {
			inv, invPriv := fixtureKey("invite-a")
			l.invite(alicePriv, inv, 0, time.Hour)
			l.append(alicePriv, wireEntry{Type: TypeRotate, Author: b64(alicePub), Epoch: 1,
				Envelopes: []wireEnvelope{{Recipient: b64(alicePub), HPKECiphertext: b64([]byte("ct-for-alice"))}},
				KeyChain:  b64([]byte("key-chain-0-to-1"))})
			l.join(bobPriv, "bob", inv, invPriv, 1)
		}},
		{"join_expired", false, func(l *linkLog) {
			inv, invPriv := fixtureKey("invite-a")
			l.invite(alicePriv, inv, 0, time.Hour)
			l.at += 2 * time.Hour // the join's ts is past expires
			l.join(bobPriv, "bob", inv, invPriv, 0)
		}},
		{"join_consumed_invite", false, func(l *linkLog) {
			inv, invPriv := fixtureKey("invite-a")
			l.invite(alicePriv, inv, 0, time.Hour)
			l.join(bobPriv, "bob", inv, invPriv, 0)
			l.join(carolPriv, "carol", inv, invPriv, 0)
		}},
		{"join_bad_proof", false, func(l *linkLog) {
			inv, _ := fixtureKey("invite-a")
			_, wrongPriv := fixtureKey("invite-b")
			l.invite(alicePriv, inv, 0, time.Hour)
			l.join(bobPriv, "bob", inv, wrongPriv, 0)
		}},
		{"join_proof_for_other_list", false, func(l *linkLog) {
			inv, invPriv := fixtureKey("invite-a")
			l.invite(alicePriv, inv, 0, time.Hour)
			otherList := sha256.Sum256([]byte("another list"))
			proof := ed25519.Sign(invPriv, JoinProofMessage(otherList[:], bobPub, rawAgree("bob")))
			l.append(bobPriv, wireEntry{Type: TypeJoin, Author: b64(bobPub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"),
				Invite: b64(inv), InviteProof: b64(proof)})
		}},
		{"join_author_not_subject", false, func(l *linkLog) {
			inv, invPriv := fixtureKey("invite-a")
			l.invite(alicePriv, inv, 0, time.Hour)
			proof := ed25519.Sign(invPriv, JoinProofMessage(l.rawListID(), carolPubKey, rawAgree("carol")))
			l.append(bobPriv, wireEntry{Type: TypeJoin, Author: b64(bobPub), Subject: b64(carolPubKey), SubjectAgree: agreeKey("carol"),
				Invite: b64(inv), InviteProof: b64(proof)})
		}},
		{"invite_by_non_owner", false, func(l *linkLog) {
			inv, _ := fixtureKey("invite-a")
			l.append(alicePriv, wireEntry{Type: TypeAdd, Author: b64(alicePub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"),
				Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}}})
			l.invite(bobPriv, inv, 0, time.Hour)
		}},
		{"invite_expiry_too_far", false, func(l *linkLog) {
			inv, _ := fixtureKey("invite-a")
			l.invite(alicePriv, inv, 0, MaxInviteLifetime+time.Minute)
		}},
		{"invite_key_reused", false, func(l *linkLog) {
			inv, _ := fixtureKey("invite-a")
			l.invite(alicePriv, inv, 0, time.Hour)
			l.uninvite(alicePriv, inv, 0)
			l.invite(alicePriv, inv, 0, time.Hour)
		}},
		{"entry_for_other_group", false, func(l *linkLog) {
			l.group = groupIDFromGenesis([]byte("another genesis"))
			l.append(alicePriv, wireEntry{Type: TypeAdd, Author: b64(alicePub), Subject: b64(bobPub), SubjectAgree: agreeKey("bob"),
				Envelopes: []wireEnvelope{{Recipient: b64(bobPub), HPKECiphertext: b64([]byte("ct-for-bob"))}}})
		}},
	}
	for i, c := range linkCases {
		l := newLinkLog(t, alicePriv, baseTs.Add(time.Duration(23+i)*24*time.Hour))
		c.build(l)
		writeFixture(t, dir, fixture{Name: c.name, Valid: c.valid, Entries: l.entries})
	}

	// --- Genesis envelopes (rule 8): create seals K_0 to its author only. ---
	for i, c := range []struct {
		name      string
		valid     bool
		recipient ed25519.PublicKey
	}{
		{"create_envelope_owner_valid", true, alicePub},
		{"create_envelope_not_author", false, bobPub},
	} {
		genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(alicePub), AuthorAgree: agreeKey("alice"), Epoch: 0,
			Envelopes: []wireEnvelope{{Recipient: b64(c.recipient), HPKECiphertext: b64([]byte("ct-k0"))}},
			Ts:        baseTs.Add(time.Duration(40+i) * 24 * time.Hour).UnixMilli()}
		raw, sig := signEntry(t, alicePriv, genesis)
		writeFixture(t, dir, fixture{Name: c.name, Valid: c.valid, Entries: []fixtureEntry{{Raw: raw, Sig: sig}}})
	}

	t.Logf("wrote fixtures to %s", dir)
}

// linkLog builds one list's log entry by entry: each append fills in
// group, seq, prev and a ts one minute after the previous entry.
type linkLog struct {
	t       *testing.T
	group   string
	entries []fixtureEntry
	start   time.Time
	at      time.Duration
}

func newLinkLog(t *testing.T, ownerPriv ed25519.PrivateKey, start time.Time) *linkLog {
	owner := ownerPriv.Public().(ed25519.PublicKey)
	genesis := wireEntry{V: 1, Seq: 0, Type: TypeCreate, Author: b64(owner), AuthorAgree: agreeKey("alice"), Epoch: 0, Ts: start.UnixMilli()}
	raw, sig := signEntry(t, ownerPriv, genesis)
	return &linkLog{t: t, group: groupIDFromGenesis(raw), entries: []fixtureEntry{{Raw: raw, Sig: sig}}, start: start}
}

func (l *linkLog) rawListID() []byte {
	b, _ := hex.DecodeString(groupIDFromGenesis(l.entries[0].Raw))
	return b
}

func (l *linkLog) append(priv ed25519.PrivateKey, w wireEntry) {
	l.at += time.Minute
	prev := l.entries[len(l.entries)-1].Raw
	w.V, w.Group, w.Seq, w.Prev, w.Ts = 1, l.group, int64(len(l.entries)), hashB64(prev), l.start.Add(l.at).UnixMilli()
	raw, sig := signEntry(l.t, priv, w)
	l.entries = append(l.entries, fixtureEntry{Raw: raw, Sig: sig})
}

// invite opens a link invite expiring `lifetime` after the entry's ts.
func (l *linkLog) invite(owner ed25519.PrivateKey, inv ed25519.PublicKey, epoch int64, lifetime time.Duration) {
	expires := l.start.Add(l.at + time.Minute + lifetime).UnixMilli()
	l.append(owner, wireEntry{Type: TypeInvite, Author: b64(owner.Public().(ed25519.PublicKey)), Epoch: epoch, Invite: b64(inv), Expires: expires})
}

func (l *linkLog) uninvite(owner ed25519.PrivateKey, inv ed25519.PublicKey, epoch int64) {
	l.append(owner, wireEntry{Type: TypeUninvite, Author: b64(owner.Public().(ed25519.PublicKey)), Epoch: epoch, Invite: b64(inv)})
}

// join admits the holder of joinerPriv with the proof invPriv signs.
func (l *linkLog) join(joinerPriv ed25519.PrivateKey, label string, inv ed25519.PublicKey, invPriv ed25519.PrivateKey, epoch int64) {
	joiner := joinerPriv.Public().(ed25519.PublicKey)
	proof := ed25519.Sign(invPriv, JoinProofMessage(l.rawListID(), joiner, rawAgree(label)))
	l.append(joinerPriv, wireEntry{Type: TypeJoin, Author: b64(joiner), Subject: b64(joiner), SubjectAgree: agreeKey(label),
		Epoch: epoch, Invite: b64(inv), InviteProof: b64(proof)})
}

func rawAgree(label string) []byte {
	k := sha256.Sum256([]byte("grouplog-fixture-agree:" + label))
	return k[:]
}

func fixtureKey(label string) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := sha256.Sum256([]byte("grouplog-fixture:" + label))
	priv := ed25519.NewKeyFromSeed(seed[:])
	return priv.Public().(ed25519.PublicKey), priv
}

// agreeKey is a fixed stand-in X25519 public key: validators only check
// its presence and length, and servers that it matches the account.
func agreeKey(label string) string {
	k := sha256.Sum256([]byte("grouplog-fixture-agree:" + label))
	return b64(k[:])
}

func signEntry(t *testing.T, priv ed25519.PrivateKey, w wireEntry) (raw, sig []byte) {
	t.Helper()
	raw, err := CanonicalJSON(w)
	require.NoError(t, err)
	return raw, ed25519.Sign(priv, raw)
}

func hashB64(raw []byte) string {
	h := sha256.Sum256(raw)
	return b64(h[:])
}

func groupIDFromGenesis(genesisRaw []byte) string {
	h := sha256.Sum256(genesisRaw)
	return hex.EncodeToString(h[:])
}

func fixturesDir(t *testing.T) string {
	t.Helper()
	// internal/grouplog -> server/ -> repo root -> protocol/log-fixtures
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "protocol", "log-fixtures"))
	require.NoError(t, err)
	return dir
}

type fixtureEntry struct {
	Raw []byte `json:"raw"` // the exact signed bytes, base64url via MarshalJSON below
	Sig []byte `json:"sig"`
}

type fixture struct {
	Name    string         `json:"name"`
	Valid   bool           `json:"valid"`
	Entries []fixtureEntry `json:"entries"`
}

func writeFixture(t *testing.T, dir string, f fixture) {
	t.Helper()
	data, err := json.MarshalIndent(f, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, f.Name+".json"), append(data, '\n'), 0o644))
}
