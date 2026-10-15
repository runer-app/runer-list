//! Behaviour beyond the vectors: round trips, binding to context, tampering
//! and canonical forms. Ported from the former TypeScript tests.

use runer_crypto::account::*;
use runer_crypto::content::*;
use runer_crypto::encoding::encode_hex;
use runer_crypto::invite::*;
use runer_crypto::list_keys::*;
use runer_crypto::primitives::random_bytes;
use runer_crypto::safety::safety_code;
use runer_crypto::{SigningKeyPair, ed25519_verify};

fn flip(b: &[u8]) -> Vec<u8> {
    let mut c = b.to_vec();
    *c.last_mut().unwrap() ^= 1;
    c
}

fn list_hex() -> String {
    encode_hex(&random_bytes::<32>())
}

fn public(id: &Identity) -> PublicIdentity {
    PublicIdentity { sign_pub: id.sign.public_key().to_vec(), agree_pub: id.agree.public_key().to_vec() }
}

mod account_key {
    use super::*;

    #[test]
    fn round_trips_through_12_words_tolerating_case_and_spacing() {
        let key = generate_account_key();
        let words = account_key_to_words(&key[..]).unwrap();
        assert_eq!(words.len(), 12);
        let typed = format!("  {} \n", words.join("   ").to_uppercase());
        assert_eq!(words_to_account_key(&typed).unwrap(), key);
    }

    #[test]
    fn rejects_typos_wrong_counts_and_bad_checksums() {
        let words = account_key_to_words(&generate_account_key()[..]).unwrap();
        assert!(words_to_account_key(&words[..11].join(" ")).is_none());
        assert!(words_to_account_key(&format!("{} notaword", words[..11].join(" "))).is_none());
        // All-zero entropy is "abandon ×11 about"; "abandon ×12" has the wrong checksum.
        assert_eq!(words_to_account_key(&format!("{}about", "abandon ".repeat(11))).unwrap()[..], [0u8; 16]);
        assert!(words_to_account_key(&"abandon ".repeat(12)).is_none());
    }

    #[test]
    fn derives_distinct_stable_auth_and_wrap_keys() {
        let key = generate_account_key();
        let a1 = derive_auth_key(&key[..]).unwrap();
        assert_eq!(a1.public_key(), derive_auth_key(&key[..]).unwrap().public_key());
        assert_ne!(derive_wrap_key(&key[..]).unwrap()[..], a1.seed()[..]);
        assert!(derive_auth_key(&[0u8; 15]).is_err());
    }

    #[test]
    fn builds_the_login_message_as_the_server_does() {
        let msg = ServerMessage::Login.bytes("https://x.test", &[7u8; 32]).unwrap();
        let expected = [&b"runer/v1/login"[..], &[0, 0, 14], b"https://x.test", &[7u8; 32]].concat();
        assert_eq!(msg, expected);
    }
}

#[test]
fn account_settings_round_trip_and_open_only_for_their_identity() {
    let id = Identity::generate();
    let json = r#"{"v":1,"clock":"24h"}"#;
    let sealed = seal_account_settings(&id, json, None).unwrap();
    assert_eq!(sealed.len(), 12 + json.len() + 16);
    assert_eq!(open_account_settings(&id, &sealed).unwrap(), json);
    assert!(open_account_settings(&id, &flip(&sealed)).is_err());
    assert!(open_account_settings(&Identity::generate(), &sealed).is_err());
    // The key is the agreement key's, the AD names the signing key: neither half alone opens it.
    let mixed = Identity::from_secrets(&Identity::generate().sign.seed()[..], &id.agree.private_key()[..]).unwrap();
    assert!(open_account_settings(&mixed, &sealed).is_err());
}

#[test]
fn identity_bundle_round_trips_and_is_bound_to_auth_pub_and_bundle_gen() {
    let ak = generate_account_key();
    let wrap = derive_wrap_key(&ak[..]).unwrap();
    let auth_pub = derive_auth_key(&ak[..]).unwrap().public_key();
    let id = Identity::generate();
    let bundle = seal_identity_bundle(&wrap[..], &auth_pub, 3, &id, None).unwrap();
    let back = open_identity_bundle(&wrap[..], &auth_pub, 3, &bundle).unwrap();
    assert_eq!(back.sign.public_key(), id.sign.public_key());
    assert_eq!(back.agree.public_key(), id.agree.public_key());

    assert!(open_identity_bundle(&wrap[..], &auth_pub, 2, &bundle).is_err());
    assert!(open_identity_bundle(&wrap[..], &flip(&auth_pub), 3, &bundle).is_err());
    assert!(open_identity_bundle(&wrap[..], &auth_pub, 3, &flip(&bundle)).is_err());
}

mod list_keys {
    use super::*;

    #[test]
    fn chains_each_epoch_back_to_the_previous_key() {
        let (k0, k1) = (generate_list_key(), generate_list_key());
        let chain = seal_key_chain(&k1[..], &k0[..]).unwrap();
        assert_eq!(chain.len(), 48);
        assert_eq!(open_key_chain(&k1[..], &chain).unwrap(), k0);
        assert!(open_key_chain(&k0[..], &chain).is_err());
    }

    #[test]
    fn separates_the_document_key_from_the_list_key() {
        let k = generate_list_key();
        assert_ne!(document_key(&k[..]).unwrap(), k);
    }

    #[test]
    fn opens_an_envelope_only_for_its_recipient_list_and_epoch() {
        let (bob, carol) = (Identity::generate(), Identity::generate());
        let k = generate_list_key();
        let list = list_id_bytes(&list_hex()).unwrap();
        let (bob_sign, bob_agree) = (bob.sign.public_key(), bob.agree.public_key());
        let to = EnvelopeTarget { sign_pub: &bob_sign, agree_pub: &bob_agree };
        let env = seal_envelope(&list, 4, &to, &k[..]).unwrap();
        assert_eq!(env.len(), 80);
        let bob_priv = bob.agree.private_key();
        assert_eq!(open_envelope(&list, 4, &bob_sign, &bob_priv[..], &env).unwrap(), k);

        assert!(open_envelope(&list, 5, &bob_sign, &bob_priv[..], &env).is_err());
        assert!(open_envelope(&flip(&list), 4, &bob_sign, &bob_priv[..], &env).is_err());
        assert!(open_envelope(&list, 4, &carol.sign.public_key(), &bob_priv[..], &env).is_err());
        assert!(open_envelope(&list, 4, &bob_sign, &carol.agree.private_key()[..], &env).is_err());
        assert!(open_envelope(&list, 4, &bob_sign, &bob_priv[..], &flip(&env)).is_err());
    }

    #[test]
    fn rejects_malformed_list_ids() {
        let list = list_hex();
        assert!(list_id_bytes(&list.to_uppercase()).is_err());
        assert!(list_id_bytes(&list[2..]).is_err());
    }
}

mod content {
    use super::*;

    fn setup() -> (SigningKeyPair, Vec<u8>, ContentHeader) {
        let alice = SigningKeyPair::generate();
        let doc_key = document_key(&generate_list_key()[..]).unwrap().to_vec();
        let header = ContentHeader {
            content_type: ContentType::Update,
            list: list_hex(),
            epoch: 2,
            covers_seq: None,
            author: alice.public_key().to_vec(),
            log_head: random_bytes::<32>().to_vec(),
        };
        (alice, doc_key, header)
    }

    #[test]
    fn round_trips_an_update_and_a_snapshot() {
        let (alice, doc_key, header) = setup();
        let sealed = seal_content(&doc_key, &alice, &header, b"update", None).unwrap();
        let (h, body) = open_content(&doc_key, &sealed).unwrap();
        assert_eq!(h, header);
        assert_eq!(&body[..], b"update");

        let snap_header = ContentHeader { content_type: ContentType::Snapshot, covers_seq: Some(9), ..header };
        let snap = seal_content(&doc_key, &alice, &snap_header, b"state", None).unwrap();
        assert_eq!(open_content(&doc_key, &snap).unwrap().0.covers_seq, Some(9));
    }

    #[test]
    fn rejects_tampering_with_any_part() {
        let (alice, doc_key, header) = setup();
        let s = seal_content(&doc_key, &alice, &header, b"x", None).unwrap();
        let err = |m: SealedContent| open_content(&doc_key, &m).unwrap_err().to_string();
        assert_eq!(err(SealedContent { ct: flip(&s.ct), ..s.clone() }), "bad content signature");
        assert_eq!(err(SealedContent { nonce: flip(&s.nonce), ..s.clone() }), "bad content signature");
        assert_eq!(err(SealedContent { sig: flip(&s.sig), ..s.clone() }), "bad content signature");
        let epoch3 = encode_content_header(&ContentHeader { epoch: 3, ..header }).unwrap();
        assert_eq!(err(SealedContent { header: epoch3, ..s.clone() }), "bad content signature");
        // A validly signed message under another key fails to decrypt.
        let other = document_key(&generate_list_key()[..]).unwrap();
        assert_eq!(open_content(&other[..], &s).unwrap_err().to_string(), "decryption failed");
    }

    #[test]
    fn writes_the_header_in_canonical_order_and_rejects_any_other_form() {
        let (_, _, header) = setup();
        let snap = ContentHeader { content_type: ContentType::Snapshot, covers_seq: Some(1), ..header.clone() };
        let text = String::from_utf8(encode_content_header(&snap).unwrap()).unwrap();
        let keys: Vec<usize> = ["\"type\"", "\"list\"", "\"epoch\"", "\"covers_seq\"", "\"author\"", "\"log_head\""]
            .iter()
            .map(|k| text.find(k).unwrap())
            .collect();
        assert!(keys.is_sorted(), "{text}");

        let good = String::from_utf8(encode_content_header(&header).unwrap()).unwrap();
        for bad in [
            good.replace("\"epoch\":2", "\"epoch\":2.0"),
            good.replacen('{', "{ ", 1),
            good.replace("\"type\":\"update\",", "\"type\":\"update\",\"type\":\"update\","),
            good.replace("\"type\":\"update\"", "\"type\":\"\\u0075pdate\""),
            good.replace('}', ",\"x\":1}"),
        ] {
            assert!(parse_content_header(bad.as_bytes()).is_err(), "{bad}");
        }
        assert!(encode_content_header(&ContentHeader { covers_seq: Some(1), ..header.clone() }).is_err());
        assert!(encode_content_header(&ContentHeader { content_type: ContentType::Snapshot, ..header }).is_err());
    }
}

#[test]
fn invite_lets_the_link_holder_read_the_package_and_prove_their_keys() {
    let (alice, bob) = (Identity::generate(), Identity::generate());
    let keys = derive_invite_keys(&generate_invite_secret()[..]).unwrap();
    assert_eq!(keys.id.len(), 16);

    let pkg = InvitePackage {
        list: list_hex(),
        log_head: random_bytes::<32>().to_vec(),
        owner: public(&alice),
        name: "Groceries".into(),
        owner_name: String::new(),
        key: None,
    };
    let sealed = seal_invite_package(&keys, &pkg, None).unwrap();
    assert_eq!(open_invite_package(&keys, &sealed).unwrap(), pkg);

    let name = seal_join_name(&keys, "Bob", None).unwrap();
    assert_eq!(open_join_name(&keys, &name).unwrap(), "Bob");
    // Package and join ciphertexts share a key but not their AD, so one can't pass for the other.
    assert!(open_join_name(&keys, &sealed).is_err());
    assert!(open_invite_package(&keys, &name).is_err());

    let joiner = public(&bob);
    let proof = invite_proof(&keys, &joiner).unwrap();
    assert!(verify_invite_proof(&keys, &joiner, &proof).unwrap());
    let swapped = PublicIdentity { agree_pub: alice.agree.public_key().to_vec(), ..joiner.clone() };
    assert!(!verify_invite_proof(&keys, &swapped, &proof).unwrap());
    let other = derive_invite_keys(&generate_invite_secret()[..]).unwrap();
    assert!(!verify_invite_proof(&other, &joiner, &proof).unwrap());
    assert!(open_invite_package(&other, &sealed).is_err());
}

mod link_invite {
    use super::*;

    #[test]
    fn derives_one_invite_key_per_secret_and_binds_the_proof_to_list_and_keys() {
        let (bob, eve) = (Identity::generate(), Identity::generate());
        let secret = generate_invite_secret();
        let k = derive_invite_sign_key(&secret[..]).unwrap();
        assert_eq!(derive_invite_sign_key(&secret[..]).unwrap().public_key(), k.public_key());
        assert_ne!(derive_invite_sign_key(&generate_invite_secret()[..]).unwrap().public_key(), k.public_key());

        let list = list_id_bytes(&list_hex()).unwrap();
        let joiner = public(&bob);
        let proof = sign_join_proof(&k, &list, &joiner).unwrap();
        assert!(verify_join_proof(&k.public_key(), &list, &joiner, &proof));
        assert!(!verify_join_proof(&k.public_key(), &flip(&list), &joiner, &proof));
        let swapped = PublicIdentity { agree_pub: eve.agree.public_key().to_vec(), ..joiner.clone() };
        assert!(!verify_join_proof(&k.public_key(), &list, &swapped, &proof));
        assert!(!verify_join_proof(&eve.sign.public_key(), &list, &joiner, &proof));
    }

    #[test]
    fn carries_the_list_key_in_the_package_and_only_as_a_pair() {
        let alice = Identity::generate();
        let keys = derive_invite_keys(&generate_invite_secret()[..]).unwrap();
        let pkg = InvitePackage {
            list: list_hex(),
            log_head: random_bytes::<32>().to_vec(),
            owner: public(&alice),
            name: "Groceries".into(),
            owner_name: "Alice".into(),
            key: Some(PackageKey { epoch: 3, list_key: zeroize::Zeroizing::new(generate_list_key().to_vec()) }),
        };
        let sealed = seal_invite_package(&keys, &pkg, None).unwrap();
        assert_eq!(open_invite_package(&keys, &sealed).unwrap(), pkg);

        let text = String::from_utf8(encode_invite_package(&pkg).unwrap().to_vec()).unwrap();
        let tail = r#","name":"Groceries","owner_name":"Alice","epoch":3,"list_key":""#;
        let cut = text.find(tail).unwrap() + tail.len() - r#","list_key":""#.len();
        assert!(text.ends_with("\"}"));
        let no_key = format!("{}}}", &text[..cut]);
        assert!(parse_invite_package(no_key.as_bytes()).is_err());

        // The owner's name is left out when empty, and only then: each package has one form.
        let nameless = InvitePackage { owner_name: String::new(), ..pkg.clone() };
        let bare = text.replace(r#","owner_name":"Alice""#, "");
        assert_eq!(parse_invite_package(bare.as_bytes()).unwrap(), nameless);
        assert_eq!(encode_invite_package(&nameless).unwrap()[..], *bare.as_bytes());
        let empty = text.replace(r#""owner_name":"Alice""#, r#""owner_name":"""#);
        assert!(parse_invite_package(empty.as_bytes()).is_err());
    }
}

#[test]
fn safety_code_is_six_groups_of_five_digits_that_change_with_either_key() {
    let (a, b) = (Identity::generate(), Identity::generate());
    let code = safety_code(&a.sign.public_key(), &a.agree.public_key()).unwrap();
    assert!(code.iter().all(|g| g.len() == 5 && g.bytes().all(|c| c.is_ascii_digit())));
    assert_ne!(safety_code(&a.sign.public_key(), &b.agree.public_key()).unwrap(), code);
    assert_ne!(safety_code(&b.sign.public_key(), &a.agree.public_key()).unwrap(), code);
}

#[test]
fn verifies_what_it_signs_and_rejects_bad_keys_without_panicking() {
    let k = SigningKeyPair::generate();
    let sig = k.sign(b"msg");
    assert!(ed25519_verify(&k.public_key(), b"msg", &sig));
    assert!(!ed25519_verify(&[0xff; 32], b"msg", &sig));
    assert!(!ed25519_verify(&k.public_key(), b"msg", &sig[1..]));
    assert!(!ed25519_verify(&k.public_key()[1..], b"msg", &sig));
}
