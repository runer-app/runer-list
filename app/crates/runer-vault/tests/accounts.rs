//! Accounts (E2EE §3, §9): sign-up, a second device, change of account key,
//! and which key signs which server message.

use runer_crypto::ed25519_verify;
use runer_crypto::primitives::sha256;
use runer_vault::{ServerMessage, Vault, VaultError, VaultWarning};

const SERVER: &str = "https://runer.test";
const NONCE: [u8; 32] = [9; 32];

fn verifies(public: &[u8], kind: ServerMessage, sig: &[u8]) -> bool {
    ed25519_verify(public, &kind.bytes(SERVER, &NONCE).unwrap(), sig)
}

/// A second device: sign in with the words and open the bundle the server holds.
fn second_device(words: &[String], bundle_gen: u64, bundle: &[u8]) -> (Vault, String) {
    let mut v = Vault::new();
    let (acct, _) = v.sign_in(SERVER, &words.join(" ")).unwrap();
    v.load_identity(&acct, bundle_gen, bundle).unwrap();
    (v, acct)
}

#[test]
fn sign_up_then_sign_in_elsewhere_opens_the_same_identity() {
    let mut v = Vault::new();
    let s = v.start_sign_up(SERVER);

    let login = v.sign_server_message(&s.account, ServerMessage::Login, &NONCE).unwrap();
    assert!(verifies(&s.auth_pub, ServerMessage::Login, &login));
    let identity_sig = v.sign_server_message(&s.account, ServerMessage::Identity, &NONCE).unwrap();
    assert!(verifies(&s.identity.sign_pub, ServerMessage::Identity, &identity_sig));

    let pow = v.solve_sign_up_pow(&s.account, &NONCE, 12).unwrap();
    let h = sha256(&[&s.auth_pub[..], &NONCE, &pow].concat());
    assert_eq!((h[0], h[1] >> 4), (0, 0), "12 leading zero bits");

    let words = v.confirm_sign_up(&s.account).unwrap();
    assert_eq!(words.len(), 12);
    assert_eq!(v.show_words(&s.account).unwrap(), words);
    assert!(matches!(v.confirm_sign_up(&s.account), Err(VaultError::InvalidState(_))));
    assert!(
        matches!(v.sign_server_message(&s.account, ServerMessage::Identity, &NONCE), Err(VaultError::InvalidState(_))),
        "a registered identity signs no more identity messages"
    );

    let (mut d2, acct2) = second_device(&words, 1, &s.bundle);
    assert_eq!(d2.load_identity(&acct2, 1, &s.bundle).unwrap().0, s.identity);
}

#[test]
fn a_bundle_for_another_key_or_generation_does_not_open() {
    let mut v = Vault::new();
    let s = v.start_sign_up(SERVER);
    let words = v.confirm_sign_up(&s.account).unwrap();
    let mut other = Vault::new();
    let (acct, _) = other.sign_in(SERVER, &words.join(" ")).unwrap();
    assert_eq!(other.load_identity(&acct, 2, &s.bundle), Err(VaultError::BadBundle));
    let o = other.start_sign_up(SERVER);
    assert_eq!(other.load_identity(&acct, 1, &o.bundle), Err(VaultError::BadBundle));
}

#[test]
fn change_account_key_keeps_the_identity_and_the_old_key_until_commit() {
    let mut v = Vault::new();
    let s = v.start_sign_up(SERVER);
    let old_words = v.confirm_sign_up(&s.account).unwrap();

    let r = v.change_account_key(&s.account).unwrap();
    let rekey = v.sign_server_message(&s.account, ServerMessage::Rekey, &NONCE).unwrap();
    assert!(verifies(&s.auth_pub, ServerMessage::Rekey, &rekey), "the rekey is signed with the old key");
    assert_eq!(v.show_words(&s.account).unwrap(), old_words);

    let words = v.commit_account_key(&s.account).unwrap();
    assert_ne!(words, old_words);
    let login = v.sign_server_message(&s.account, ServerMessage::Login, &NONCE).unwrap();
    assert!(verifies(&r.auth_pub, ServerMessage::Login, &login));
    assert!(matches!(v.commit_account_key(&s.account), Err(VaultError::InvalidState(_))));

    let (mut d2, acct2) = second_device(&words, 2, &r.bundle);
    assert_eq!(d2.load_identity(&acct2, 2, &r.bundle).unwrap().0, s.identity);
}

#[test]
fn a_lower_bundle_gen_is_a_rollback_and_changes_nothing() {
    let mut v = Vault::new();
    let s = v.start_sign_up(SERVER);
    v.confirm_sign_up(&s.account).unwrap();
    let r = v.change_account_key(&s.account).unwrap();
    let words = v.commit_account_key(&s.account).unwrap();

    let (mut d2, acct2) = second_device(&words, 2, &r.bundle);
    let (id, warnings) = d2.load_identity(&acct2, 1, &s.bundle).unwrap();
    assert_eq!(id, s.identity, "the identity loaded at generation 2 stays");
    assert_eq!(warnings, [VaultWarning::BundleRollback]);
}

#[test]
fn guards() {
    let mut v = Vault::new();
    assert_eq!(v.show_words("acct_nope"), Err(VaultError::UnknownAccount));
    let s = v.start_sign_up(SERVER);
    assert!(matches!(
        v.sign_server_message(&s.account, ServerMessage::Login, &[0; 31]),
        Err(VaultError::InvalidArgument(_))
    ));
    assert!(matches!(v.solve_sign_up_pow(&s.account, &NONCE, 33), Err(VaultError::InvalidArgument(_))));

    let mut fresh = Vault::new();
    let (acct, _) = fresh.sign_in(SERVER, &v.show_words(&s.account).unwrap().join(" ")).unwrap();
    assert!(matches!(fresh.change_account_key(&acct), Err(VaultError::InvalidState(_))), "no identity loaded yet");

    v.sign_out(&s.account).unwrap();
    assert_eq!(v.show_words(&s.account), Err(VaultError::UnknownAccount));
}

#[test]
fn the_account_settings_open_on_a_second_device_and_after_a_rekey() {
    let mut v = Vault::new();
    let s = v.start_sign_up(SERVER);
    let words = v.confirm_sign_up(&s.account).unwrap();
    let json = r#"{"v":1,"clock":"12h"}"#;
    let sealed = v.seal_account_settings(&s.account, json).unwrap();
    assert_eq!(v.open_account_settings(&s.account, &sealed).unwrap(), json);

    let (other, acct) = second_device(&words, 1, &s.bundle);
    assert_eq!(other.open_account_settings(&acct, &sealed).unwrap(), json);

    v.change_account_key(&s.account).unwrap();
    v.commit_account_key(&s.account).unwrap();
    assert_eq!(v.open_account_settings(&s.account, &sealed).unwrap(), json, "the settings follow the identity");

    let mut stranger = Vault::new();
    let t = stranger.start_sign_up(SERVER);
    assert!(matches!(stranger.open_account_settings(&t.account, &sealed), Err(VaultError::InvalidArgument(_))));
}
