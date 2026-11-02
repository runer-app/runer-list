//! Content (E2EE §7): the vault seals under the current epoch with a header
//! it writes itself, and opens only what a member of that epoch signed.

use runer_crypto::SigningKeyPair;
use runer_crypto::content::{ContentHeader, ContentType, SealedContent, seal_content};
use runer_vault::log::SignedEntry;
use runer_vault::{ContentKind, Rejected, Vault, VaultError};
use std::time::{SystemTime, UNIX_EPOCH};

const SERVER: &str = "https://runer.test";

/// Two devices of one fresh account, both holding a new list.
fn shared_list() -> (Vault, String, Vault, String, String) {
    let mut a = Vault::new();
    let s = a.start_sign_up(SERVER);
    let words = a.confirm_sign_up(&s.account).unwrap();
    let mut b = Vault::new();
    let (acct_b, _) = b.sign_in(SERVER, &words.join(" ")).unwrap();
    b.load_identity(&acct_b, 1, &s.bundle).unwrap();
    let list = a.create_list(&s.account).unwrap();
    for (v, acct) in [(&mut a, &s.account), (&mut b, &acct_b)] {
        v.apply_log(acct, &list.id, std::slice::from_ref(&list.entry)).unwrap();
    }
    (a, s.account, b, acct_b, list.id)
}

fn apply(v: &mut Vault, acct: &str, list: &str, e: &SignedEntry) {
    v.apply_log(acct, list, std::slice::from_ref(e)).unwrap();
}

fn rejection(v: &Vault, acct: &str, list: &str, m: SealedContent) -> Rejected {
    match v.open_contents(acct, list, &[m]).unwrap().pop().unwrap() {
        Ok(_) => panic!("opened"),
        Err(r) => r,
    }
}

#[test]
fn another_device_opens_what_one_sealed_with_the_header_the_vault_wrote() {
    let (a, acct_a, b, acct_b, list) = shared_list();
    let m = a.seal_content(&acct_a, &list, ContentKind::Update, b"hello").unwrap();
    let header = String::from_utf8(m.header.clone()).unwrap();
    assert!(header.starts_with(&format!(r#"{{"type":"update","list":"{list}","epoch":0,"author":"#)), "{header}");

    let opened = b.open_contents(&acct_b, &list, &[m]).unwrap().pop().unwrap().unwrap();
    assert_eq!(&opened.body[..], b"hello");
    assert_eq!((opened.epoch, opened.covers_seq), (0, None));
    assert_eq!(opened.author.to_vec(), b.accounts()[0].identity.sign_pub.to_vec());
}

#[test]
fn snapshots_carry_what_they_cover() {
    let (a, acct_a, _, _, list) = shared_list();
    let m = a.seal_content(&acct_a, &list, ContentKind::Snapshot { covers_seq: 7 }, b"state").unwrap();
    assert!(String::from_utf8(m.header.clone()).unwrap().contains(r#""epoch":0,"covers_seq":7,"#));
    let opened = a.open_contents(&acct_a, &list, &[m]).unwrap().pop().unwrap().unwrap();
    assert_eq!(opened.covers_seq, Some(7));
}

#[test]
fn after_a_rotate_new_content_uses_the_new_epoch_and_old_content_still_opens() {
    let (mut a, acct_a, mut b, acct_b, list) = shared_list();
    let old = a.seal_content(&acct_a, &list, ContentKind::Update, b"before").unwrap();
    let r = a.rotate(&acct_a, &list).unwrap();
    apply(&mut a, &acct_a, &list, &r);
    apply(&mut b, &acct_b, &list, &r);
    let new = a.seal_content(&acct_a, &list, ContentKind::Update, b"after").unwrap();
    let opened = b.open_contents(&acct_b, &list, &[old, new]).unwrap();
    let got: Vec<_> = opened.into_iter().map(|o| o.map(|o| (o.epoch, o.body.to_vec()))).collect();
    assert_eq!(got, vec![Ok((0, b"before".to_vec())), Ok((1, b"after".to_vec()))]);
}

#[test]
fn a_bad_message_is_rejected_on_its_own() {
    let (a, acct_a, b, acct_b, list) = shared_list();
    let good = a.seal_content(&acct_a, &list, ContentKind::Update, b"ok").unwrap();
    let mut flipped = good.clone();
    *flipped.ct.last_mut().unwrap() ^= 1;
    let mut bad_sig = good.clone();
    bad_sig.sig[0] ^= 1;
    let mut not_canonical = good.clone();
    not_canonical.header.insert(1, b' ');
    let results = b.open_contents(&acct_b, &list, &[flipped, good, bad_sig, not_canonical]).unwrap();
    let got: Vec<_> = results.into_iter().map(|r| r.map(|o| o.body.to_vec())).collect();
    assert_eq!(
        got,
        vec![Err(Rejected::BadContent), Ok(b"ok".to_vec()), Err(Rejected::BadContent), Err(Rejected::Malformed)]
    );
}

#[test]
fn content_for_another_list_or_by_a_non_member_is_rejected() {
    let (mut a, acct_a, _, _, list) = shared_list();
    let other = a.create_list(&acct_a).unwrap();
    apply(&mut a, &acct_a, &other.id, &other.entry);
    let elsewhere = a.seal_content(&acct_a, &other.id, ContentKind::Update, b"x").unwrap();
    assert_eq!(rejection(&a, &acct_a, &list, elsewhere), Rejected::WrongList);

    // Signed by a key the log never admitted, with a well-formed header for this list.
    let stranger = SigningKeyPair::generate();
    let header = ContentHeader {
        content_type: ContentType::Update,
        list: list.clone(),
        epoch: 0,
        covers_seq: None,
        author: stranger.public_key().to_vec(),
        log_head: vec![0; 32],
    };
    let forged = seal_content(&[7; 32], &stranger, &header, b"x", None).unwrap();
    assert_eq!(rejection(&a, &acct_a, &list, forged), Rejected::NotMember);
}

#[test]
fn the_vault_seals_only_for_lists_it_has_verified() {
    let (a, acct_a, _, _, _) = shared_list();
    let unknown = "00".repeat(32);
    assert!(matches!(a.seal_content(&acct_a, &unknown, ContentKind::Update, b"x"), Err(VaultError::UnknownList)));
    assert!(matches!(a.open_contents(&acct_a, &unknown, &[]), Err(VaultError::UnknownList)));
}

#[test]
fn opened_content_says_whether_its_log_head_is_in_the_verified_log() {
    let (mut a, acct_a, mut b, acct_b, list) = shared_list();
    let at_genesis = a.seal_content(&acct_a, &list, ContentKind::Update, b"one").unwrap();
    // An entry that keeps the epoch, so the other device can open what follows before it has the entry.
    let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis() as u64;
    let invite = a.create_invite(&acct_a, &list, now + 3_600_000, "List", "Alice").unwrap().entry;
    apply(&mut a, &acct_a, &list, &invite);
    let ahead = a.seal_content(&acct_a, &list, ContentKind::Update, b"two").unwrap();

    let known = |v: &Vault, acct: &str| -> Vec<bool> {
        let opened = v.open_contents(acct, &list, &[at_genesis.clone(), ahead.clone()]).unwrap();
        opened.into_iter().map(|o| o.unwrap().head_known).collect()
    };
    assert_eq!(known(&b, &acct_b), vec![true, false]);
    apply(&mut b, &acct_b, &list, &invite);
    assert_eq!(known(&b, &acct_b), vec![true, true]);
}
