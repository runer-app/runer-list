//! The shared crypto vectors (`protocol/vectors` at the repo
//! root, checked by the Go server too). Every value the crate can compute
//! from the vector's inputs must come out byte for byte; sealed envelopes,
//! whose ephemeral key the `hpke` crate can't take as input, must open.

use runer_crypto::account::*;
use runer_crypto::content::*;
use runer_crypto::invite::*;
use runer_crypto::list_keys::*;
use runer_crypto::safety::safety_code;
use runer_crypto::{SigningKeyPair, ed25519_verify};
use serde_json::Value;
use std::path::PathBuf;

fn load(file: &str) -> Value {
    let dir = std::env::var_os("VECTORS_DIR")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../protocol/vectors"));
    serde_json::from_str(&std::fs::read_to_string(dir.join(file)).unwrap()).unwrap()
}

fn unhex(v: &Value) -> Vec<u8> {
    hex::decode(v.as_str().unwrap()).unwrap()
}

fn nonce(v: &Value) -> [u8; 12] {
    unhex(v).try_into().unwrap()
}

fn text(v: &Value) -> &str {
    v.as_str().unwrap()
}

fn msg_kind(name: &str) -> ServerMessage {
    match name {
        "login" => ServerMessage::Login,
        "rekey" => ServerMessage::Rekey,
        "identity" => ServerMessage::Identity,
        _ => panic!("unknown message {name}"),
    }
}

#[test]
fn account() {
    for c in load("account.json")["cases"].as_array().unwrap() {
        let ak = words_to_account_key(text(&c["words"])).unwrap();
        assert_eq!(ak.to_vec(), unhex(&c["account_key"]));
        assert_eq!(account_key_to_words(&ak[..]).unwrap().join(" "), text(&c["words"]));

        let auth = derive_auth_key(&ak[..]).unwrap();
        assert_eq!(auth.seed().to_vec(), unhex(&c["auth_seed"]));
        assert_eq!(auth.public_key().to_vec(), unhex(&c["auth_pub"]));
        assert_eq!(derive_wrap_key(&ak[..]).unwrap().to_vec(), unhex(&c["wrap_key"]));

        for (name, m) in c["messages"].as_object().unwrap() {
            let msg = msg_kind(name).bytes(text(&c["server_url"]), &unhex(&c["nonce"])).unwrap();
            assert_eq!(msg, unhex(&m["message"]), "{name}");
            assert_eq!(auth.sign(&msg).to_vec(), unhex(&m["auth_sig"]), "{name}");
            assert!(ed25519_verify(&auth.public_key(), &msg, &unhex(&m["auth_sig"])));
        }
    }
}

#[test]
fn identity_bundle() {
    for c in load("identity-bundle.json")["cases"].as_array().unwrap() {
        let (wrap, auth_pub, generation) =
            (unhex(&c["wrap_key"]), unhex(&c["auth_pub"]), c["bundle_gen"].as_u64().unwrap());
        let id = Identity::from_secrets(&unhex(&c["sign_seed"]), &unhex(&c["agree_private"])).unwrap();
        assert_eq!(id.sign.public_key().to_vec(), unhex(&c["sign_pub"]));
        assert_eq!(id.agree.public_key().to_vec(), unhex(&c["agree_pub"]));
        let sealed = seal_identity_bundle(&wrap, &auth_pub, generation, &id, Some(&nonce(&c["nonce"]))).unwrap();
        assert_eq!(sealed, unhex(&c["bundle"]));

        let back = open_identity_bundle(&wrap, &auth_pub, generation, &unhex(&c["bundle"])).unwrap();
        assert_eq!(back.sign.public_key().to_vec(), unhex(&c["sign_pub"]));
        assert_eq!(back.agree.public_key().to_vec(), unhex(&c["agree_pub"]));
    }
}

#[test]
fn list_keys_and_envelopes() {
    let v = load("list-keys.json");
    let epochs = v["epochs"].as_array().unwrap();
    for (i, e) in epochs.iter().enumerate() {
        assert_eq!(document_key(&unhex(&e["list_key"])).unwrap().to_vec(), unhex(&e["document_key"]));
        if i > 0 {
            let (k, prev) = (unhex(&e["list_key"]), unhex(&epochs[i - 1]["list_key"]));
            assert_eq!(seal_key_chain(&k, &prev).unwrap(), unhex(&e["key_chain"]));
            assert_eq!(open_key_chain(&k, &unhex(&e["key_chain"])).unwrap().to_vec(), prev);
        }
    }
    for e in v["envelopes"].as_array().unwrap() {
        let list = list_id_bytes(text(&e["list"])).unwrap();
        let epoch = e["epoch"].as_u64().unwrap();
        let sign_pub = unhex(&e["recipient_sign_pub"]);
        assert_eq!(envelope_info(&list, epoch, &sign_pub).unwrap(), unhex(&e["info"]));
        let key = open_envelope(
            &list,
            epoch,
            &sign_pub,
            &unhex(&e["recipient_agree_private"]),
            &unhex(&e["hpke_ciphertext"]),
        )
        .unwrap();
        assert_eq!(key.to_vec(), unhex(&e["list_key"]), "{}", text(&e["name"]));
        // The recorded ephemeral key is consistent: its public half is `enc`.
        let eph = runer_crypto::AgreementKeyPair::from_private(&unhex(&e["ephemeral_private"])).unwrap();
        assert_eq!(eph.public_key().to_vec(), unhex(&e["ephemeral_pub"]));
        assert_eq!(unhex(&e["hpke_ciphertext"])[..32], unhex(&e["ephemeral_pub"])[..]);
    }
}

#[test]
fn content() {
    for c in load("content.json")["cases"].as_array().unwrap() {
        let header = parse_content_header(text(&c["header_text"]).as_bytes()).unwrap();
        assert_eq!(encode_content_header(&header).unwrap(), unhex(&c["header"]));
        let author = SigningKeyPair::from_seed(&unhex(&c["author_seed"])).unwrap();
        let doc_key = unhex(&c["document_key"]);
        let sealed = seal_content(&doc_key, &author, &header, &unhex(&c["body"]), Some(&nonce(&c["nonce"]))).unwrap();
        assert_eq!(sealed.ct, unhex(&c["ct"]));
        assert_eq!(sealed.sig, unhex(&c["sig"]));
        let (h, body) = open_content(&doc_key, &sealed).unwrap();
        assert_eq!(h, header);
        assert_eq!(body.to_vec(), unhex(&c["body"]));
    }
}

#[test]
fn invite() {
    let v = load("invite.json");
    let keys = derive_invite_keys(&unhex(&v["secret"])).unwrap();
    assert_eq!(keys.id.to_vec(), unhex(&v["id"]));
    assert_eq!(keys.enc.to_vec(), unhex(&v["enc"]));
    assert_eq!(keys.mac.to_vec(), unhex(&v["mac"]));

    let pkg = parse_invite_package(text(&v["package_text"]).as_bytes()).unwrap();
    assert_eq!(seal_invite_package(&keys, &pkg, Some(&nonce(&v["package_nonce"]))).unwrap(), unhex(&v["package"]));
    assert_eq!(open_invite_package(&keys, &unhex(&v["package"])).unwrap(), pkg);

    let name = text(&v["display_name"]);
    assert_eq!(seal_join_name(&keys, name, Some(&nonce(&v["join_nonce"]))).unwrap(), unhex(&v["join_name"]));
    assert_eq!(open_join_name(&keys, &unhex(&v["join_name"])).unwrap(), name);

    let joiner = PublicIdentity { sign_pub: unhex(&v["joiner_sign_pub"]), agree_pub: unhex(&v["joiner_agree_pub"]) };
    assert_eq!(invite_proof(&keys, &joiner).unwrap().to_vec(), unhex(&v["invite_proof"]));
    assert!(verify_invite_proof(&keys, &joiner, &unhex(&v["invite_proof"])).unwrap());

    let link = &v["link"];
    let sign_key = derive_invite_sign_key(&unhex(&v["secret"])).unwrap();
    assert_eq!(sign_key.seed().to_vec(), unhex(&link["invite_sign_seed"]));
    assert_eq!(sign_key.public_key().to_vec(), unhex(&link["invite_pub"]));
    let pkg = parse_invite_package(text(&link["package_text"]).as_bytes()).unwrap();
    let key = pkg.key.as_ref().unwrap();
    assert_eq!(key.epoch, link["epoch"].as_u64().unwrap());
    assert_eq!(key.list_key.to_vec(), unhex(&link["list_key"]));
    assert_eq!(
        seal_invite_package(&keys, &pkg, Some(&nonce(&link["package_nonce"]))).unwrap(),
        unhex(&link["package"])
    );

    let list = list_id_bytes(&pkg.list).unwrap();
    assert_eq!(join_proof_message(&list, &joiner).unwrap(), unhex(&link["join_message"]));
    assert_eq!(sign_join_proof(&sign_key, &list, &joiner).unwrap().to_vec(), unhex(&link["join_proof"]));
    assert!(verify_join_proof(&unhex(&link["invite_pub"]), &list, &joiner, &unhex(&link["join_proof"])));
}

#[test]
fn safety_codes() {
    for c in load("safety-code.json")["cases"].as_array().unwrap() {
        let code = safety_code(&unhex(&c["sign_pub"]), &unhex(&c["agree_pub"])).unwrap();
        assert_eq!(code.join(" "), text(&c["code"]));
    }
}
