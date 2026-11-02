//! Validity rules beyond the shared fixtures (E2EE §4.2), and the parser's
//! own checks.

mod common;

use common::*;
use runer_vault::log::{LogErrorCode as C, Role, SignedEntry, WireEntry, apply_entries, parse_entry};

/// Runs `candidate` on top of the builder's log: `Ok` or the rule it broke.
fn verdict(log: &LogBuilder, candidate: SignedEntry) -> Result<(), C> {
    let mut all = log.entries.clone();
    all.push(candidate);
    apply_entries(None, &all).map(|_| ()).map_err(|e| e.code)
}

/// A list with the owner and Anna, plus Bob and Eve outside it.
fn setup() -> (Id, Id, Id, Id, LogBuilder) {
    let (owner, anna, bob, eve) = (Id::new(), Id::new(), Id::new(), Id::new());
    let mut log = LogBuilder::create(owner);
    let e = log.add(&anna, none);
    log.append(e);
    (owner, anna, bob, eve, log)
}

#[test]
fn accepts_a_full_lifecycle_add_leave_rotate_remove() {
    let (owner, anna, bob, _, mut log) = setup();
    let e = log.add(&bob, none);
    log.append(e);
    let e = log.leave(&bob, none);
    log.append(e);
    let e = log.rotate(&anna, &[owner, anna], none);
    log.append(e);
    log.epoch = 1;
    let e = log.remove(&anna, &[owner], none);
    log.append(e);
    let state = apply_entries(None, &log.entries).unwrap().0.unwrap();
    assert_eq!(state.epoch, 2);
    assert_eq!(state.members.values().map(|m| m.role).collect::<Vec<_>>(), [Role::Owner]);
}

#[test]
fn rule_3_a_member_cannot_add_or_remove() {
    let (owner, anna, bob, _, mut log) = setup();
    let e = log.add_by(&anna, &bob, none);
    assert_eq!(verdict(&log, e), Err(C::NotOwner));
    let e = log.remove_by(&anna, &owner, &[anna], none);
    assert_eq!(verdict(&log, e), Err(C::NotOwner));
}

#[test]
fn rule_3_only_members_rotate() {
    let (owner, anna, _, eve, mut log) = setup();
    let e = log.rotate(&eve, &[owner, anna], none);
    assert_eq!(verdict(&log, e), Err(C::NotMember));
}

#[test]
fn rule_4_rotate_needs_epoch_plus_one_a_key_chain_and_an_envelope_per_member() {
    let (owner, anna, _, _, mut log) = setup();
    let e = log.rotate(&owner, &[owner, anna], |w| w.epoch = 0);
    assert_eq!(verdict(&log, e), Err(C::EpochMustIncrease));
    let e = log.rotate(&owner, &[owner, anna], |w| w.epoch = 2);
    assert_eq!(verdict(&log, e), Err(C::EpochMustIncrease));
    let e = log.rotate(&owner, &[owner, anna], |w| w.key_chain.clear());
    assert_eq!(verdict(&log, e), Err(C::MissingKeyChain));
    let e = log.rotate(&owner, &[owner], none);
    assert_eq!(verdict(&log, e), Err(C::MissingEnvelope));
    let e = log.rotate(&owner, &[owner, anna], none);
    assert_eq!(verdict(&log, e), Ok(()));
}

#[test]
fn rule_4_remove_needs_an_envelope_for_every_remaining_member_not_the_removed_one() {
    let (owner, anna, bob, _, mut log) = setup();
    let e = log.add(&bob, none);
    log.append(e);
    let e = log.remove(&bob, &[owner], none); // anna missing
    assert_eq!(verdict(&log, e), Err(C::MissingEnvelope));
    let e = log.remove(&bob, &[owner, anna], none);
    assert_eq!(verdict(&log, e), Ok(()));
}

#[test]
fn rule_4_removing_a_non_member_fails() {
    let (owner, _, _, eve, mut log) = setup();
    let e = log.remove(&eve, &[owner], none);
    assert_eq!(verdict(&log, e), Err(C::NotMember));
    let e = log.remove(&eve, &[owner], |w| w.subject.clear());
    assert_eq!(verdict(&log, e), Err(C::NotMember), "no subject at all, as in Go");
}

#[test]
fn rule_5_add_and_leave_keep_the_epoch_add_needs_the_subject_envelope() {
    let (_, anna, bob, _, mut log) = setup();
    let e = log.add(&bob, |w| w.epoch = 1);
    assert_eq!(verdict(&log, e), Err(C::EpochMustHold));
    let e = log.add(&bob, |w| w.envelopes.clear());
    assert_eq!(verdict(&log, e), Err(C::MissingEnvelope));
    let e = log.add(&bob, |w| w.subject.clear());
    assert_eq!(verdict(&log, e), Err(C::MissingEnvelope), "no subject at all, as in Go");
    let e = log.leave(&anna, |w| w.epoch = 1);
    assert_eq!(verdict(&log, e), Err(C::EpochMustHold));
}

#[test]
fn rule_5_add_of_an_existing_member_is_a_conflict() {
    let (_, anna, _, _, mut log) = setup();
    let e = log.add(&anna, none);
    assert_eq!(verdict(&log, e), Err(C::AlreadyMember));
}

#[test]
fn rule_6_the_owner_cannot_leave_or_be_removed_leave_must_be_self() {
    let (owner, anna, _, _, mut log) = setup();
    let e = log.leave(&owner, none);
    assert_eq!(verdict(&log, e), Err(C::OwnerStays));
    let e = log.remove(&owner, &[anna], none);
    assert_eq!(verdict(&log, e), Err(C::OwnerStays));
    let e = log.leave(&anna, |w| w.subject = owner.b64());
    assert_eq!(verdict(&log, e), Err(C::LeaveNotSelf));
    let e = log.leave(&anna, none);
    assert_eq!(verdict(&log, e), Ok(()));
}

#[test]
fn rule_6_a_stranger_cannot_leave() {
    let (_, _, _, eve, mut log) = setup();
    let e = log.leave(&eve, none);
    assert_eq!(verdict(&log, e), Err(C::NotMember));
}

#[test]
fn rule_7_ts_may_go_back_up_to_24_h_not_further() {
    let (owner, _, _, _, mut log) = setup();
    let last = log.ts;
    // ts passes, so the next check (an envelope for Anna) is what fails.
    let e = log.rotate(&owner, &[owner], |w| w.ts = last - DAY);
    assert_eq!(verdict(&log, e), Err(C::MissingEnvelope));
    let e = log.rotate(&owner, &[owner], |w| w.ts = last - DAY - 1);
    assert_eq!(verdict(&log, e), Err(C::TimestampSkew));
}

#[test]
fn rule_1_a_wrong_seq_is_a_head_mismatch() {
    let (_, anna, _, _, mut log) = setup();
    let e = log.leave(&anna, |w| w.seq = 5);
    assert_eq!(verdict(&log, e), Err(C::HeadMismatch));
}

#[test]
fn rule_1_the_group_field_must_be_the_list_id_after_genesis() {
    let (_, anna, _, _, mut log) = setup();
    let e = log.leave(&anna, |w| w.group = "00".repeat(32));
    assert_eq!(verdict(&log, e), Err(C::Malformed));
}

#[test]
fn rule_2_a_log_must_start_with_create_genesis_epoch_must_be_0() {
    let (owner, anna, _, _, mut log) = setup();
    let add = log.add(&anna, none);
    assert_eq!(apply_entries(None, &[add]).unwrap_err().code, C::HeadMismatch);
    let g = sign_entry(
        &owner,
        WireEntry {
            v: 1,
            r#type: "create".into(),
            author: owner.b64(),
            author_agree: b(&owner.agree),
            epoch: 1,
            ts: 1,
            ..Default::default()
        },
    );
    assert_eq!(apply_entries(None, &[g]).unwrap_err().code, C::EpochMustHold);
}

#[test]
fn the_genesis_seals_k0_to_its_author_only() {
    let (owner, other) = (Id::new(), Id::new());
    let genesis = |envelopes: Vec<_>| {
        sign_entry(
            &owner,
            WireEntry {
                v: 1,
                r#type: "create".into(),
                author: owner.b64(),
                author_agree: b(&owner.agree),
                envelopes,
                ts: 1,
                ..Default::default()
            },
        )
    };
    assert!(apply_entries(None, &[genesis(vec![envelope(&owner)])]).is_ok());
    let bad = [vec![envelope(&other)], vec![envelope(&owner), envelope(&owner)]];
    for envelopes in bad {
        assert_eq!(apply_entries(None, &[genesis(envelopes)]).unwrap_err().code, C::Malformed);
    }
}

#[test]
fn unknown_entry_types_parse_but_fail_validation_like_go() {
    let (owner, _, _, _, mut log) = setup();
    let e = log.rotate(&owner, &[owner], |w| w.r#type = "promote".into());
    assert_eq!(parse_entry(&e.raw, &e.sig).unwrap().r#type, "promote");
    assert_eq!(verdict(&log, e), Err(C::Malformed));
}

// Link invites (rules 8–12).

#[test]
fn accepts_invite_join_a_second_invite_uninvite() {
    let (_, _, bob, eve, mut log) = setup();
    let (k1, k2) = (Id::invite_key(), Id::invite_key());
    let e = log.invite(&k1, 48 * HOUR, none);
    log.append(e);
    let e = log.join(&bob, &k1, none);
    log.append(e);
    let e = log.invite(&k2, 48 * HOUR, none);
    log.append(e);
    let e = log.uninvite(&k2, none);
    log.append(e);
    let state = apply_entries(None, &log.entries).unwrap().0.unwrap();
    assert_eq!(state.members[&bob.public].role, Role::Member);
    assert!(state.invites.values().all(|i| !i.open));
    let e = log.join(&eve, &k1, none);
    assert_eq!(verdict(&log, e), Err(C::InviteNotOpen));
}

#[test]
fn rule_9_expiry_must_be_after_ts_and_at_most_7_days_on() {
    let (_, _, _, _, mut log) = setup();
    let k = Id::invite_key();
    let e = log.invite(&k, 0, none);
    assert_eq!(verdict(&log, e), Err(C::InviteExpiry));
    let e = log.invite(&k, 7 * DAY, none);
    assert_eq!(verdict(&log, e), Ok(()));
    let e = log.invite(&k, 7 * DAY + 1, none);
    assert_eq!(verdict(&log, e), Err(C::InviteExpiry));
}

#[test]
fn rule_11_a_join_exactly_at_expiry_is_still_in_time() {
    let (_, _, bob, _, mut log) = setup();
    let k = Id::invite_key();
    let e = log.invite(&k, 5000, none);
    log.append(e);
    let expires = log.ts + 5000;
    let e = log.join(&bob, &k, |w| w.ts = expires);
    assert_eq!(verdict(&log, e), Ok(()));
    let e = log.join(&bob, &k, |w| w.ts = expires + 1);
    assert_eq!(verdict(&log, e), Err(C::InviteExpired));
}

#[test]
fn rules_3_10_11_owner_only_uninvite_only_open_invites_no_member_joins_twice() {
    let (_, anna, bob, _, mut log) = setup();
    let k = Id::invite_key();
    let e = log.uninvite(&k, none);
    assert_eq!(verdict(&log, e), Err(C::InviteNotOpen));
    let e = log.invite(&k, 48 * HOUR, none);
    log.append(e);
    let e = log.uninvite_by(&anna, &k, none);
    assert_eq!(verdict(&log, e), Err(C::NotOwner));
    let e = log.join(&anna, &k, none);
    assert_eq!(verdict(&log, e), Err(C::AlreadyMember));
    let e = log.join(&bob, &k, |w| w.epoch = 1);
    assert_eq!(verdict(&log, e), Err(C::EpochMustHold));
}

#[test]
fn rule_8_link_invite_fields_only_where_they_belong() {
    let (owner, anna, bob, _, mut log) = setup();
    let k = Id::invite_key();
    let e = log.invite(&k, 48 * HOUR, |w| w.envelopes = vec![envelope(&anna)]);
    assert_eq!(verdict(&log, e), Err(C::Malformed));
    let ts = log.ts;
    let e = log.uninvite(&k, |w| w.expires = ts + 10_000);
    assert_eq!(verdict(&log, e), Err(C::Malformed));
    let e = log.rotate(&owner, &[owner, anna], |w| w.invite = k.b64());
    assert_eq!(verdict(&log, e), Err(C::Malformed));
    let e = log.invite(&k, 48 * HOUR, none);
    log.append(e);
    let e = log.join(&bob, &k, |w| w.invite_proof.clear());
    assert_eq!(verdict(&log, e), Err(C::Malformed));
}

// The parser and `apply_entries`.

#[test]
fn reports_the_index_of_the_offending_entry() {
    let (_, anna, _, _, mut log) = setup();
    let bad = log.leave(&anna, |w| w.seq = 9);
    let mut all = log.entries.clone();
    all.push(bad);
    assert_eq!(apply_entries(None, &all).unwrap_err().index, Some(2));
}

#[test]
fn catch_up_continues_from_a_saved_state() {
    let (_, anna, _, _, mut log) = setup();
    let (first, _) = apply_entries(None, &log.entries).unwrap();
    let leave = log.leave(&anna, none);
    let (second, _) = apply_entries(first, &[leave]).unwrap();
    assert_eq!(second.unwrap().members.len(), 1);
}

#[test]
fn rejects_a_signature_from_the_wrong_key() {
    let (owner, _, _, eve, mut log) = setup();
    let e = log.rotate(&owner, &[owner], none);
    let forged = eve.key().sign(&e.raw);
    assert_eq!(parse_entry(&e.raw, &forged).unwrap_err().code, C::InvalidSignature);
    assert_eq!(parse_entry(&e.raw, &e.sig[..63]).unwrap_err().code, C::InvalidSignature);
}

#[test]
fn rejects_invalid_utf8_a_bom_and_non_objects() {
    let sig = [0u8; 64];
    for raw in [&[0xff, 0xfe][..], "\u{feff}{}".as_bytes(), b"[]", b"{\"v\":1} x"] {
        assert_eq!(parse_entry(raw, &sig).unwrap_err().code, C::Malformed, "{raw:?}");
    }
}

#[test]
fn integers_beyond_2_to_the_53_are_out_of_range() {
    let (owner, _, _, _, mut log) = setup();
    let e = log.rotate(&owner, &[owner], |w| w.ts = 1 << 53);
    let err = parse_entry(&e.raw, &e.sig).unwrap_err();
    assert_eq!((err.code, err.detail.as_deref()), (C::Malformed, Some("integer out of range")));
    let e = log.rotate(&owner, &[owner], |w| w.ts = -1);
    assert_eq!(parse_entry(&e.raw, &e.sig).unwrap_err().code, C::Malformed);
}
