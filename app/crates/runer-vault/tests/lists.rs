//! Lists (E2EE §4, §5): create, the first rotate, keys reaching a second
//! device, and the head checks against the server.

mod common;

use common::*;
use runer_crypto::primitives::sha256;
use runer_vault::log::SignedEntry;
use runer_vault::{Vault, VaultError, VaultWarning};

const SERVER: &str = "https://runer.test";

/// Two devices of one fresh account.
fn two_devices() -> (Vault, String, Vault, String) {
    let mut a = Vault::new();
    let s = a.start_sign_up(SERVER);
    let words = a.confirm_sign_up(&s.account).unwrap();
    let mut b = Vault::new();
    let (acct, _) = b.sign_in(SERVER, &words.join(" ")).unwrap();
    b.load_identity(&acct, 1, &s.bundle).unwrap();
    (a, s.account, b, acct)
}

fn apply(v: &mut Vault, acct: &str, list: &str, entries: &[SignedEntry]) -> runer_vault::ListState {
    v.apply_log(acct, list, entries).unwrap().state
}

#[test]
fn a_new_list_seals_its_first_key_to_the_owner_for_every_device() {
    let (mut a, acct_a, mut b, acct_b) = two_devices();
    let list = a.create_list(&acct_a).unwrap();
    assert_eq!(list.id, runer_crypto::encoding::encode_hex(&sha256(&list.entry.raw)));
    let state = apply(&mut a, &acct_a, &list.id, std::slice::from_ref(&list.entry));
    assert_eq!((state.epoch, state.head_seq), (0, 0));
    assert!(state.created > 1_700_000_000_000);

    // The other device verifies from genesis and opens K_0: it can rotate, which needs the current key.
    assert_eq!(apply(&mut b, &acct_b, &list.id, std::slice::from_ref(&list.entry)), state);
    let first = b.rotate(&acct_b, &list.id).unwrap();
    assert_eq!(b.list_state(&acct_b, &list.id).unwrap(), state, "nothing changes before it is applied");
    apply(&mut b, &acct_b, &list.id, std::slice::from_ref(&first));
    assert_eq!(apply(&mut a, &acct_a, &list.id, &[first]).epoch, 1);
    a.rotate(&acct_a, &list.id).unwrap();
}

#[test]
fn sign_up_brings_an_inbox_owned_by_the_identity() {
    let mut v = Vault::new();
    let s = v.start_sign_up(SERVER);
    v.confirm_sign_up(&s.account).unwrap();
    let inbox = apply(&mut v, &s.account, &s.inbox.id, &[s.inbox.entry]);
    assert_eq!(inbox.owner, s.identity);
    v.rotate(&s.account, &s.inbox.id).unwrap();
}

#[test]
fn guards() {
    let (mut a, acct, _, _) = two_devices();
    let log = LogBuilder::create(Id::new());
    apply(&mut a, &acct, &log.group, &log.entries);
    assert!(matches!(a.rotate(&acct, &log.group), Err(VaultError::InvalidState(_))), "not a member");
    assert_eq!(a.rotate(&acct, &"00".repeat(32)), Err(VaultError::UnknownList));

    let mut fresh = Vault::new();
    let (other, _) = fresh.sign_in(SERVER, &a.show_words(&acct).unwrap().join(" ")).unwrap();
    assert!(matches!(fresh.create_list(&other), Err(VaultError::InvalidState(_))), "no identity loaded yet");
}

#[test]
fn check_head_reports_rollback_and_fork() {
    let (mut a, acct, _, _) = two_devices();
    let owner = Id::new();
    let mut log = LogBuilder::create(owner);
    let e = log.add(&Id::new(), none);
    log.append(e);
    apply(&mut a, &acct, &log.group, &log.entries);
    let h1 = sha256(&log.entries[1].raw);
    let h0 = sha256(&log.entries[0].raw);

    assert_eq!(a.check_head(&acct, &log.group, 1, &h1).unwrap(), []);
    assert_eq!(a.check_head(&acct, &log.group, 5, &[7; 32]).unwrap(), [], "a longer log is news");
    assert_eq!(a.check_head(&acct, &log.group, 0, &h0).unwrap(), [VaultWarning::LogRollback]);
    assert_eq!(a.check_head(&acct, &log.group, 1, &[7; 32]).unwrap(), [VaultWarning::LogFork]);
    assert_eq!(
        a.check_head(&acct, &log.group, 0, &[7; 32]).unwrap(),
        [VaultWarning::LogRollback, VaultWarning::LogFork]
    );
}

#[test]
fn forget_list_drops_it() {
    let (mut a, acct, _, _) = two_devices();
    let list = a.create_list(&acct).unwrap();
    apply(&mut a, &acct, &list.id, &[list.entry]);
    a.forget_list(&acct, &list.id).unwrap();
    assert_eq!(a.list_state(&acct, &list.id), Err(VaultError::UnknownList));
    assert_eq!(a.check_head(&acct, &list.id, 0, &[0; 32]), Err(VaultError::UnknownList));
}
