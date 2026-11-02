//! The vault's own behaviour on top of the log rules: handles, catch-up,
//! all-or-nothing batches, fork detection and the list id check.

mod common;

use common::*;
use runer_vault::log::LogErrorCode;
use runer_vault::{LogEvent, PublicId, Vault, VaultError, VaultWarning};

const WORDS: &str = "legal winner thank year wave sausage worth useful legal winner thank yellow";
const SERVER: &str = "https://runer.test";

fn signed_in() -> (Vault, String) {
    let mut v = Vault::new();
    let (h, _) = v.sign_in(SERVER, WORDS).unwrap();
    (v, h)
}

fn pid(id: &Id) -> PublicId {
    PublicId { sign_pub: id.public, agree_pub: id.agree }
}

#[test]
fn sign_in_catches_typos_and_keeps_one_handle_per_account_and_server() {
    let mut v = Vault::new();
    assert!(v.sign_in(SERVER, "legal winner thank").is_none());
    assert!(v.sign_in(SERVER, &WORDS.replace("yellow", "legal")).is_none(), "bad checksum");
    let (a, auth_pub) = v.sign_in(SERVER, WORDS).unwrap();
    let (b, _) = v.sign_in(SERVER, &WORDS.to_uppercase().replace(' ', "  ")).unwrap();
    assert_eq!(a, b);
    let (c, other_pub) = v.sign_in("https://other.test", WORDS).unwrap();
    assert_ne!(a, c);
    assert_eq!(auth_pub, other_pub, "the auth key depends on the words alone");
}

#[test]
fn applies_a_whole_log_then_catches_up() {
    let (mut v, acct) = signed_in();
    let (owner, anna, bob) = (Id::new(), Id::new(), Id::new());
    let mut log = LogBuilder::create(owner);
    let e = log.add(&anna, none);
    log.append(e);

    let got = v.apply_log(&acct, &log.group, &log.entries).unwrap();
    assert_eq!(got.events, [LogEvent::Added(anna.public)]);
    assert!(got.warnings.is_empty());
    assert_eq!(got.state.id, log.group);
    assert_eq!(got.state.owner, pid(&owner));
    assert_eq!(got.state.members, [pid(&owner), pid(&anna)]);
    assert_eq!((got.state.epoch, got.state.head_seq), (0, 1));

    let (k, stranger) = (Id::invite_key(), Id::invite_key());
    for key in [k, stranger] {
        let e = log.invite(&key, 48 * HOUR, none);
        log.append(e);
    }
    let e = log.join(&bob, &k, none);
    log.append(e);
    let e = log.leave(&anna, none);
    log.append(e);
    let e = log.rotate(&owner, &[owner, bob], none);
    log.append(e);
    let got = v.apply_log(&acct, &log.group, &log.entries[2..]).unwrap();
    assert_eq!(got.events, [LogEvent::Joined(bob.public), LogEvent::Left(anna.public), LogEvent::Rotated(1)]);
    assert_eq!(got.state.members, [pid(&owner), pid(&bob)]);
    assert_eq!((got.state.epoch, got.state.head_seq), (1, 6));
    assert!(got.state.open_invites.is_empty(), "the rotate voided the stranger's invite");
    assert_eq!(v.list_state(&acct, &log.group).unwrap(), got.state);
}

#[test]
fn entries_we_have_are_skipped_and_an_empty_batch_is_fine() {
    let (mut v, acct) = signed_in();
    let (owner, anna) = (Id::new(), Id::new());
    let mut log = LogBuilder::create(owner);
    v.apply_log(&acct, &log.group, &log.entries).unwrap();
    let e = log.add(&anna, none);
    log.append(e);

    let got = v.apply_log(&acct, &log.group, &log.entries).unwrap();
    assert_eq!(got.events, [LogEvent::Added(anna.public)]);
    let again = v.apply_log(&acct, &log.group, &log.entries).unwrap();
    assert!(again.events.is_empty());
    assert_eq!(again.state, got.state);
    assert_eq!(v.apply_log(&acct, &log.group, &[]).unwrap().state, got.state);
}

#[test]
fn a_different_entry_at_a_verified_seq_is_a_fork_and_changes_nothing() {
    let (mut v, acct) = signed_in();
    let (owner, anna, bob) = (Id::new(), Id::new(), Id::new());
    let mut log = LogBuilder::create(owner);
    let e = log.add(&anna, none);
    log.append(e);
    let before = v.apply_log(&acct, &log.group, &log.entries).unwrap().state;

    // The same genesis (the builder is deterministic), then a validly signed
    // but different entry at seq 1.
    let mut alt = LogBuilder::create(owner);
    assert_eq!(alt.group, log.group);
    let forked = [alt.entries[0].clone(), alt.add(&bob, none)];
    let got = v.apply_log(&acct, &log.group, &forked).unwrap();
    assert_eq!(got.warnings, [VaultWarning::LogFork]);
    assert!(got.events.is_empty());
    assert_eq!(got.state, before);
    assert_eq!(v.list_state(&acct, &log.group).unwrap(), before);
}

#[test]
fn a_bad_entry_anywhere_rejects_the_whole_batch() {
    let (mut v, acct) = signed_in();
    let (owner, anna, eve) = (Id::new(), Id::new(), Id::new());
    let mut log = LogBuilder::create(owner);
    v.apply_log(&acct, &log.group, &log.entries).unwrap();
    let before = v.list_state(&acct, &log.group).unwrap();
    let good = log.add(&anna, none);
    log.append(good.clone());
    let bad = log.leave(&eve, none);

    let err = v.apply_log(&acct, &log.group, &[good, bad]).unwrap_err();
    let VaultError::Log(e) = &err else { panic!("{err}") };
    assert_eq!((e.code, e.index), (LogErrorCode::NotMember, Some(1)));
    assert_eq!(err.code(), "NotMember");
    assert_eq!(v.list_state(&acct, &log.group).unwrap(), before);
}

#[test]
fn the_genesis_must_be_the_list_asked_for() {
    let (mut v, acct) = signed_in();
    let (a, b) = (LogBuilder::create(Id::new()), LogBuilder::create(Id::new()));
    assert_eq!(v.apply_log(&acct, &a.group, &b.entries), Err(VaultError::WrongList));
    assert_eq!(v.list_state(&acct, &a.group), Err(VaultError::UnknownList));
    assert_eq!(v.apply_log(&acct, &a.group, &[]), Err(VaultError::UnknownList));
    assert!(matches!(v.apply_log(&acct, "not-hex", &a.entries), Err(VaultError::InvalidArgument(_))));
}

#[test]
fn logs_belong_to_one_account() {
    let (mut v, acct) = signed_in();
    let log = LogBuilder::create(Id::new());
    assert_eq!(v.apply_log("acct_nope", &log.group, &log.entries), Err(VaultError::UnknownAccount));
    v.apply_log(&acct, &log.group, &log.entries).unwrap();
    let (other, _) = v.sign_in("https://other.test", WORDS).unwrap();
    assert_eq!(v.list_state(&other, &log.group), Err(VaultError::UnknownList));
}
