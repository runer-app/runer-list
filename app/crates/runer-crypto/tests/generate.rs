//! Rebuilds the shared crypto vectors in `protocol/vectors` at the repo root.
//! This test requires the committed files to be what the generator makes,
//! which shows the crate still reproduces them byte for byte.
//!
//! Every value is computed by this crate from the inputs below, so the
//! bytes for a changed vector are one command away:
//! `VECTORS_WRITE=1 cargo test -p runer-crypto --test generate the_committed`
//! (`VECTORS_DIR` picks another directory). A changed
//! vector counts once the server's Go check passes too (`make test-unit` in `server/`).
//!
//! The `hpke` crate won't take a raw ephemeral key, so envelopes are sealed
//! with a fixed-output RNG: `hpke` turns its output (SHA-256 of the
//! envelope's name) into the ephemeral key with `DeriveKeyPair`, and the
//! vector records that derived private key, which the Go test re-seals with.

use hpke::aead::AesGcm256;
use hpke::kdf::HkdfSha256;
use hpke::kem::X25519HkdfSha256;
use hpke::rand_core::{TryCryptoRng, TryRng};
use hpke::{Deserializable as _, Kem as _, OpModeS, Serializable as _};
use runer_crypto::account::*;
use runer_crypto::content::*;
use runer_crypto::encoding::decode_base64url;
use runer_crypto::invite::*;
use runer_crypto::labels;
use runer_crypto::list_keys::*;
use runer_crypto::primitives::sha256;
use runer_crypto::safety::safety_code;
use std::convert::Infallible;
use std::path::PathBuf;

// --- inputs ----------------------------------------------------------------

struct Person {
    name: &'static str,
    account_key: &'static str,
    /// The nonce of the server messages in `account.json`.
    nonce: &'static str,
    sign_seed: &'static str,
    agree_private: &'static str,
}

const ALICE: Person = Person {
    name: "alice",
    account_key: "a89a98f8d8e745ad1a65e0190dfa2a2d",
    nonce: "19c85d9aecbb0bc596f80a219bd4805b5f33cfc0865dfd22d8b8cfd3976a2f04",
    sign_seed: "134e77621089028c8a4e4faa34b4d5a9203f1f24632d1a3dfef83591738690ec",
    agree_private: "cd034e29c9efa8dae9b8ff489056da5bd4bcf150ac68e03eac6ab97658565cc7",
};

const BOB: Person = Person {
    name: "bob",
    account_key: "a2ffdd9bff6cd7e2c87766e256c20e9b",
    nonce: "68b28802654c46c9ad67f58ae913c71497358f62f8f306b2fe744756d9d5baa2",
    sign_seed: "2fc05dc81a9c24eb7b6f409d387a7d6417286cac1c9352c34de4b638bee95b14",
    agree_private: "709f84b6d04b4e429a525f38e8ab9de468277bd6e58bb4d1ff70cee18ac45de4",
};

/// Only her public keys are used (the safety code): sign, agree.
const CAROL: (&str, &str) = (
    "b0f61b63e8c05bb3932ac59092e7b654613fcc7d3974b71114f3fb3fb7047370",
    "599a60bf0eb4e074d19fc6374110401466cad28b247a5deb94dff38a81fb8e48",
);

const SERVER_URL: &str = "https://api.example.test";
const LIST: &str = "b22bbf1c4c68938d8d3ef3e222486b2655434f343bffadcbb2ea1feeaedd987d";
/// `K_0`, `K_1`, `K_2`.
const LIST_KEYS: [&str; 3] = [
    "7f331827dc99788e0cf349895ae24e63708e4b80e4b6b9a7129d795241bf556b",
    "481e7219ca1e8ae7275a8e286b8398bc7f62204649132a38a92b295abd90ffa0",
    "98fd1f75f03fa1974c420dea3bb0991a75a25391cf03a1f2adaf2b4c7ea2310f",
];
/// The log head content and the approve-first package cite, and the link invite's own `invite` entry (base64url).
const LOG_HEAD: &str = "NwW1Jh2S43DkIbeQ-KOpi1CNeXaQ-C7dNL0OaPawkcM";
const INVITE_LOG_HEAD: &str = "36nDNObTFWs9v1HdjrBbcKwfYuX5fdxyy_iJpahNn2o";

/// Identity bundles: whose, `bundle_gen`, nonce.
const BUNDLES: [(&Person, u64, &str); 3] = [
    (&ALICE, 0, "9d6df6f3ba1ceb4d50e09fab"),
    (&ALICE, 1, "51359c87491565911e3f2897"),
    (&BOB, 7, "d6fce362d904f29d56676b68"),
];
/// Envelopes: to whom, at which epoch.
const ENVELOPES: [(&Person, usize); 3] = [(&ALICE, 0), (&BOB, 0), (&BOB, 2)];
/// Content by Alice at epoch 1: name, `covers_seq`, body, nonce.
const CONTENT: [(&str, Option<u64>, &str, &str); 2] = [
    ("update", None, "yjs update bytes", "56e8ad3440da873f5642dccd"),
    ("snapshot", Some(42), "yjs snapshot bytes", "2c6c131e14039b118d07d9d6"),
];

const INVITE_SECRET: &str = "ecb9362d09034c031c5bc13fc7053be6a3e32d35b5a9cf7580105b7163bf8dec";
const INVITE_LIST_NAME: &str = "Groceries & \"stuff\" <åäö>";
const INVITE_OWNER_NAME: &str = "Alice Ö";
const PACKAGE_NONCE: &str = "32c253400814f2f5b81bbfeb";
const JOIN_NAME: &str = "Bob";
const JOIN_NONCE: &str = "a4c629469596c00905118bf6";
const LINK_EPOCH: usize = 2;
const LINK_PACKAGE_NONCE: &str = "91e647529cda71ab5e2b2dfc";

// --- JSON, with keys in the order written -----------------------------------

enum J {
    S(String),
    N(u64),
    O(Vec<(&'static str, J)>),
    A(Vec<J>),
}
use J::{A, N, O};

fn s(text: impl Into<String>) -> J {
    J::S(text.into())
}

fn h(bytes: impl AsRef<[u8]>) -> J {
    J::S(hex::encode(bytes))
}

/// Two-space indented, as the files have always been (`JSON.stringify(v, null, 2)`).
fn render(j: &J, depth: usize, out: &mut String) {
    let pad = |n: usize| "  ".repeat(n);
    match j {
        J::S(text) => out.push_str(&serde_json::to_string(text).unwrap()),
        N(n) => out.push_str(&n.to_string()),
        O(fields) => {
            out.push_str("{\n");
            for (i, (key, value)) in fields.iter().enumerate() {
                out.push_str(&format!("{}\"{key}\": ", pad(depth + 1)));
                render(value, depth + 1, out);
                out.push_str(if i + 1 < fields.len() { ",\n" } else { "\n" });
            }
            out.push_str(&format!("{}}}", pad(depth)));
        }
        A(items) => {
            out.push_str("[\n");
            for (i, item) in items.iter().enumerate() {
                out.push_str(&pad(depth + 1));
                render(item, depth + 1, out);
                out.push_str(if i + 1 < items.len() { ",\n" } else { "\n" });
            }
            out.push_str(&format!("{}]", pad(depth)));
        }
    }
}

// --- the vectors -------------------------------------------------------------

fn unhex(text: &str) -> Vec<u8> {
    hex::decode(text).unwrap()
}

fn nonce(text: &str) -> [u8; 12] {
    unhex(text).try_into().unwrap()
}

fn identity(p: &Person) -> Identity {
    Identity::from_secrets(&unhex(p.sign_seed), &unhex(p.agree_private)).unwrap()
}

fn public(p: &Person) -> PublicIdentity {
    let id = identity(p);
    PublicIdentity { sign_pub: id.sign.public_key().to_vec(), agree_pub: id.agree.public_key().to_vec() }
}

fn account() -> J {
    let case = |p: &Person| {
        let key = unhex(p.account_key);
        let auth = derive_auth_key(&key).unwrap();
        let message = |name: &'static str, kind: ServerMessage| {
            let msg = kind.bytes(SERVER_URL, &unhex(p.nonce)).unwrap();
            (name, O(vec![("message", h(&msg)), ("auth_sig", h(auth.sign(&msg)))]))
        };
        O(vec![
            ("name", s(p.name)),
            ("account_key", h(&key)),
            ("words", s(account_key_to_words(&key).unwrap().join(" "))),
            ("auth_seed", h(&auth.seed()[..])),
            ("auth_pub", h(auth.public_key())),
            ("wrap_key", h(&derive_wrap_key(&key).unwrap()[..])),
            ("server_url", s(SERVER_URL)),
            ("nonce", s(p.nonce)),
            (
                "messages",
                O(vec![
                    message("login", ServerMessage::Login),
                    message("rekey", ServerMessage::Rekey),
                    message("identity", ServerMessage::Identity),
                ]),
            ),
        ])
    };
    O(vec![
        (
            "description",
            s(
                "Account Key → auth and wrap keys (HKDF-SHA-256, empty salt, info = label, 32 bytes; the auth output is the Ed25519 seed), and the signed server messages label ‖ 0x00 ‖ uint16be(len(url)) ‖ url ‖ nonce. E2EE §3.",
            ),
        ),
        (
            "labels",
            O(vec![
                ("auth", s(labels::AUTH)),
                ("wrap", s(labels::WRAP)),
                ("login", s(labels::LOGIN)),
                ("rekey", s(labels::REKEY)),
                ("identity", s(labels::IDENTITY)),
            ]),
        ),
        ("cases", A(vec![case(&ALICE), case(&BOB)])),
    ])
}

fn identity_bundle() -> J {
    let case = |&(p, generation, n): &(&Person, u64, &str)| {
        let key = unhex(p.account_key);
        let (wrap, auth_pub) = (derive_wrap_key(&key).unwrap(), derive_auth_key(&key).unwrap().public_key());
        let id = identity(p);
        let bundle = seal_identity_bundle(&wrap[..], &auth_pub, generation, &id, Some(&nonce(n))).unwrap();
        O(vec![
            ("name", s(format!("{}, bundle_gen {generation}", p.name))),
            ("wrap_key", h(&wrap[..])),
            ("auth_pub", h(auth_pub)),
            ("bundle_gen", N(generation)),
            ("sign_seed", s(p.sign_seed)),
            ("sign_pub", h(id.sign.public_key())),
            ("agree_private", s(p.agree_private)),
            ("agree_pub", h(id.agree.public_key())),
            ("nonce", s(n)),
            ("bundle", h(bundle)),
        ])
    };
    O(vec![
        (
            "description",
            s(
                "Identity Bundle = nonce(12) ‖ AES-256-GCM(wrap_key, 0x01 ‖ sign_seed ‖ agree_private, AD = auth_pub ‖ uint64be(bundle_gen)). E2EE §3.3.",
            ),
        ),
        ("cases", A(BUNDLES.iter().map(case).collect())),
    ])
}

/// An RNG that gives out `ikm` once: all HPKE asks for is the keying material of the ephemeral key.
struct Once<'a>(Option<&'a [u8]>);

impl TryRng for Once<'_> {
    type Error = Infallible;
    fn try_next_u32(&mut self) -> Result<u32, Infallible> {
        unreachable!("HPKE fills bytes")
    }
    fn try_next_u64(&mut self) -> Result<u64, Infallible> {
        unreachable!("HPKE fills bytes")
    }
    fn try_fill_bytes(&mut self, dst: &mut [u8]) -> Result<(), Infallible> {
        dst.copy_from_slice(self.0.take().expect("one ephemeral key per envelope"));
        Ok(())
    }
}

impl TryCryptoRng for Once<'_> {}

/// As `seal_envelope`, with the ephemeral key derived from `ikm`. Returns that key's private half and `hpke_ciphertext`.
fn seal_envelope_from(ikm: &[u8], info: &[u8], agree_pub: &[u8], list_key: &[u8]) -> (Vec<u8>, Vec<u8>) {
    type Kem = X25519HkdfSha256;
    let pk = <Kem as hpke::Kem>::PublicKey::from_bytes(agree_pub).unwrap();
    let (enc, ct) = hpke::single_shot_seal_with_rng::<AesGcm256, HkdfSha256, Kem>(
        &OpModeS::Base,
        &pk,
        info,
        list_key,
        &[],
        &mut Once(Some(ikm)),
    )
    .unwrap();
    let (ephemeral, _) = Kem::derive_keypair(ikm);
    (ephemeral.to_bytes().to_vec(), [&enc.to_bytes()[..], &ct].concat())
}

fn list_keys() -> J {
    let list = list_id_bytes(LIST).unwrap();
    let epoch = |e: usize| {
        let key = unhex(LIST_KEYS[e]);
        let mut fields = vec![
            ("epoch", N(e as u64)),
            ("list_key", s(LIST_KEYS[e])),
            ("document_key", h(&document_key(&key).unwrap()[..])),
        ];
        if e > 0 {
            fields.push(("key_chain", h(seal_key_chain(&key, &unhex(LIST_KEYS[e - 1])).unwrap())));
        }
        O(fields)
    };
    let envelope = |&(p, e): &(&Person, usize)| {
        let to = public(p);
        let name = format!("{}, epoch {e}", p.name);
        let info = envelope_info(&list, e as u64, &to.sign_pub).unwrap();
        let ikm = sha256(format!("vector envelope: {name}").as_bytes());
        let (ephemeral, sealed) = seal_envelope_from(&ikm, &info, &to.agree_pub, &unhex(LIST_KEYS[e]));
        O(vec![
            ("name", s(name)),
            ("list", s(LIST)),
            ("epoch", N(e as u64)),
            ("recipient_sign_pub", h(&to.sign_pub)),
            ("recipient_agree_pub", h(&to.agree_pub)),
            ("recipient_agree_private", s(p.agree_private)),
            ("ephemeral_private", h(ephemeral)),
            ("ephemeral_pub", h(&sealed[..32])),
            ("info", h(info)),
            ("list_key", s(LIST_KEYS[e])),
            ("hpke_ciphertext", h(&sealed)),
        ])
    };
    O(vec![
        (
            "description",
            s(format!(
                "document_key = HKDF(K_e, info=\"{}\"); key_chain(e) = AES-256-GCM(HKDF(K_e, info=\"{}\"), nonce = 12 zero bytes, K_{{e-1}}, empty AD) (ciphertext ‖ tag, 48 bytes). Envelopes: HPKE base mode DHKEM(X25519, HKDF-SHA256)/HKDF-SHA256/AES-256-GCM, info = \"{}\" ‖ 0x00 ‖ list_id(32 raw) ‖ uint64be(epoch) ‖ recipient identity_sign_pub(32), empty AAD, plaintext K_e; hpke_ciphertext = enc(32) ‖ ciphertext ‖ tag. E2EE §5.",
                labels::LIST,
                labels::CHAIN,
                labels::ENVELOPE
            )),
        ),
        ("labels", O(vec![("list", s(labels::LIST)), ("chain", s(labels::CHAIN)), ("envelope", s(labels::ENVELOPE))])),
        ("list", s(LIST)),
        ("epochs", A((0..LIST_KEYS.len()).map(epoch).collect())),
        ("envelopes", A(ENVELOPES.iter().map(envelope).collect())),
    ])
}

fn content() -> J {
    const EPOCH: usize = 1;
    let author = identity(&ALICE).sign;
    let doc_key = document_key(&unhex(LIST_KEYS[EPOCH])).unwrap();
    let case = |&(name, covers_seq, body, n): &(&'static str, Option<u64>, &str, &str)| {
        let header = ContentHeader {
            content_type: if covers_seq.is_some() { ContentType::Snapshot } else { ContentType::Update },
            list: LIST.into(),
            epoch: EPOCH as u64,
            covers_seq,
            author: author.public_key().to_vec(),
            log_head: decode_base64url(LOG_HEAD).unwrap(),
        };
        let sealed = seal_content(&doc_key[..], &author, &header, body.as_bytes(), Some(&nonce(n))).unwrap();
        O(vec![
            ("name", s(name)),
            ("document_key", h(&doc_key[..])),
            ("author_seed", s(ALICE.sign_seed)),
            ("header_text", s(String::from_utf8(sealed.header.clone()).unwrap())),
            ("header", h(&sealed.header)),
            ("body", h(body)),
            ("nonce", s(n)),
            ("ct", h(&sealed.ct)),
            ("sig", h(&sealed.sig)),
        ])
    };
    O(vec![
        (
            "description",
            s(
                "header = canonical JSON {type, list, epoch, [covers_seq on snapshots], author, log_head} (list hex, author and log_head base64url); ct = AES-256-GCM(document_key, nonce, body, AD = header) (ciphertext ‖ tag); sig = Ed25519(author, header ‖ nonce ‖ ct). E2EE §7.",
            ),
        ),
        ("cases", A(CONTENT.iter().map(case).collect())),
    ])
}

fn invite() -> J {
    let secret = unhex(INVITE_SECRET);
    let keys = derive_invite_keys(&secret).unwrap();
    let joiner = public(&BOB);
    let list = list_id_bytes(LIST).unwrap();
    let package = |log_head: &str, key: Option<PackageKey>| InvitePackage {
        list: LIST.into(),
        log_head: decode_base64url(log_head).unwrap(),
        owner: public(&ALICE),
        name: INVITE_LIST_NAME.into(),
        owner_name: if key.is_some() { INVITE_OWNER_NAME.into() } else { String::new() },
        key,
    };
    let text = |p: &InvitePackage| s(String::from_utf8(encode_invite_package(p).unwrap().to_vec()).unwrap());

    let approve_first = package(LOG_HEAD, None);
    let link_key = PackageKey { epoch: LINK_EPOCH as u64, list_key: unhex(LIST_KEYS[LINK_EPOCH]).into() };
    let link = package(INVITE_LOG_HEAD, Some(link_key));
    let sign_key = derive_invite_sign_key(&secret).unwrap();
    O(vec![
        (
            "description",
            s(format!(
                "id = HKDF(s, info=\"{}\", 16 bytes) (sent to the server as base64url); enc, mac = HKDF(s, info=\"{}\"/\"-mac\", 32 bytes). package = nonce(12) ‖ AES-256-GCM(enc, canonical JSON, AD = \"{}\" ‖ 0x00 ‖ id). join name = nonce(12) ‖ AES-256-GCM(enc, UTF-8 display name, AD = \"{}\" ‖ 0x00 ‖ id). invite_proof = HMAC-SHA-256(mac, id ‖ joiner sign_pub ‖ joiner agree_pub) (approve-first). E2EE §6, §6.2. Link invites: see \"link\".",
                labels::INVITE_ID,
                labels::INVITE_ENC,
                labels::INVITE_PACKAGE,
                labels::INVITE_JOIN
            )),
        ),
        (
            "labels",
            O(vec![
                ("invite_id", s(labels::INVITE_ID)),
                ("invite_enc", s(labels::INVITE_ENC)),
                ("invite_mac", s(labels::INVITE_MAC)),
                ("invite_package", s(labels::INVITE_PACKAGE)),
                ("invite_join", s(labels::INVITE_JOIN)),
            ]),
        ),
        ("secret", s(INVITE_SECRET)),
        ("id", h(keys.id)),
        ("enc", h(&keys.enc[..])),
        ("mac", h(&keys.mac[..])),
        ("package_text", text(&approve_first)),
        ("package_nonce", s(PACKAGE_NONCE)),
        ("package", h(seal_invite_package(&keys, &approve_first, Some(&nonce(PACKAGE_NONCE))).unwrap())),
        ("display_name", s(JOIN_NAME)),
        ("join_nonce", s(JOIN_NONCE)),
        ("join_name", h(seal_join_name(&keys, JOIN_NAME, Some(&nonce(JOIN_NONCE))).unwrap())),
        ("joiner_sign_pub", h(&joiner.sign_pub)),
        ("joiner_agree_pub", h(&joiner.agree_pub)),
        ("invite_proof", h(invite_proof(&keys, &joiner).unwrap())),
        (
            "link",
            O(vec![
                (
                    "description",
                    s(format!(
                        "invite key seed = HKDF(s, info=\"{}\", 32 bytes), an Ed25519 seed; invite_pub is its public key. The link package adds \"owner_name\" (the owner's account name; left out when empty), then \"epoch\" and \"list_key\" (base64url K_e) after \"name\", same AEAD and AD as above. join message = \"{}\" ‖ 0x00 ‖ list_id(32 raw) ‖ joiner sign_pub ‖ joiner agree_pub; join_proof = Ed25519(invite key, join message), the join entry's invite_proof. E2EE §6.1.",
                        labels::INVITE_SIGN,
                        labels::JOIN
                    )),
                ),
                ("labels", O(vec![("invite_sign", s(labels::INVITE_SIGN)), ("join", s(labels::JOIN))])),
                ("invite_sign_seed", h(&sign_key.seed()[..])),
                ("invite_pub", h(sign_key.public_key())),
                ("epoch", N(LINK_EPOCH as u64)),
                ("list_key", s(LIST_KEYS[LINK_EPOCH])),
                ("package_text", text(&link)),
                ("package_nonce", s(LINK_PACKAGE_NONCE)),
                ("package", h(seal_invite_package(&keys, &link, Some(&nonce(LINK_PACKAGE_NONCE))).unwrap())),
                ("join_message", h(join_proof_message(&list, &joiner).unwrap())),
                ("join_proof", h(sign_join_proof(&sign_key, &list, &joiner).unwrap())),
            ]),
        ),
    ])
}

fn safety_codes() -> J {
    let case = |name: &'static str, sign_pub: &[u8], agree_pub: &[u8]| {
        O(vec![
            ("name", s(name)),
            ("sign_pub", h(sign_pub)),
            ("agree_pub", h(agree_pub)),
            ("code", s(safety_code(sign_pub, agree_pub).unwrap().join(" "))),
        ])
    };
    let of = |p: &Person| {
        let id = public(p);
        case(p.name, &id.sign_pub, &id.agree_pub)
    };
    O(vec![
        (
            "description",
            s(format!(
                "h = SHA-256(\"{}\" ‖ 0x00 ‖ sign_pub ‖ agree_pub); for i in 0..5: uint40be(h[5i:5i+5]) mod 100000, as 5 digits; groups joined by spaces. E2EE §6.",
                labels::SAFETY
            )),
        ),
        ("label", s(labels::SAFETY)),
        ("cases", A(vec![of(&ALICE), of(&BOB), case("carol", &unhex(CAROL.0), &unhex(CAROL.1))])),
    ])
}

/// Every vector file, by name, as its text.
fn generate() -> Vec<(&'static str, String)> {
    [
        ("account.json", account()),
        ("identity-bundle.json", identity_bundle()),
        ("list-keys.json", list_keys()),
        ("content.json", content()),
        ("invite.json", invite()),
        ("safety-code.json", safety_codes()),
    ]
    .into_iter()
    .map(|(file, json)| {
        let mut text = String::new();
        render(&json, 0, &mut text);
        (file, text + "\n")
    })
    .collect()
}

#[test]
fn the_committed_vectors_are_what_the_generator_makes() {
    let dir = std::env::var_os("VECTORS_DIR")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../protocol/vectors"));
    let write = std::env::var_os("VECTORS_WRITE").is_some();
    for (file, text) in generate() {
        if write {
            std::fs::write(dir.join(file), text).unwrap();
            continue;
        }
        let committed = std::fs::read_to_string(dir.join(file)).unwrap();
        assert!(
            committed == text,
            "{file} is not what the generator makes: if protocol/vectors changed, the crate must follow; if the crate changed, regenerate them and run the server check (VECTORS_WRITE=1 cargo test -p runer-crypto --test generate the_committed)"
        );
    }
}

#[test]
fn generated_envelopes_open_and_come_out_the_same_every_time() {
    let text = |file: &str| generate().into_iter().find(|(f, _)| *f == file).unwrap().1;
    assert_eq!(text("list-keys.json"), text("list-keys.json"));
    let v: serde_json::Value = serde_json::from_str(&text("list-keys.json")).unwrap();
    let field = |e: &serde_json::Value, key: &str| unhex(e[key].as_str().unwrap());
    let envelopes = v["envelopes"].as_array().unwrap();
    assert_eq!(envelopes.len(), ENVELOPES.len());
    for e in envelopes {
        let key = open_envelope(
            &list_id_bytes(LIST).unwrap(),
            e["epoch"].as_u64().unwrap(),
            &field(e, "recipient_sign_pub"),
            &field(e, "recipient_agree_private"),
            &field(e, "hpke_ciphertext"),
        )
        .unwrap();
        assert_eq!(key.to_vec(), field(e, "list_key"));
        let ephemeral = runer_crypto::AgreementKeyPair::from_private(&field(e, "ephemeral_private")).unwrap();
        assert_eq!(ephemeral.public_key().to_vec(), field(e, "ephemeral_pub"));
    }
}
