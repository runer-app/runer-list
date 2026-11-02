//! Invite link secret and package (E2EE §6), the link invite's key and join
//! proof (§6.1), and the approve-first join request (§6.2).

use crate::encoding::{decode_base64url, encode_base64url, go_json_string};
use crate::labels;
use crate::primitives::{
    CryptoError, GCM_NONCE, KEY_LEN, Result, SIG_LEN, SigningKeyPair, aes_gcm_open_box, aes_gcm_seal_box, check_safe,
    ed25519_verify, fixed, hkdf, hkdf32, hmac_sha256, hmac_sha256_verify, random_bytes, require_len,
};
use serde_json::Value;
use zeroize::Zeroizing;

/// The link secret `s`: 32 random bytes, carried in the URL fragment.
pub const INVITE_SECRET_LEN: usize = 32;
/// The invite id is the first 16 bytes of HKDF(s, "…/invite-id").
pub const INVITE_ID_LEN: usize = 16;

pub fn generate_invite_secret() -> Zeroizing<[u8; INVITE_SECRET_LEN]> {
    Zeroizing::new(random_bytes())
}

pub struct InviteKeys {
    /// Raw id bytes; the server sees `encode_base64url(id)`.
    pub id: [u8; INVITE_ID_LEN],
    pub enc: Zeroizing<[u8; 32]>,
    pub mac: Zeroizing<[u8; 32]>,
}

pub fn derive_invite_keys(secret: &[u8]) -> Result<InviteKeys> {
    require_len("invite secret", secret, INVITE_SECRET_LEN)?;
    Ok(InviteKeys {
        id: fixed("invite id", &hkdf(secret, labels::INVITE_ID.as_bytes(), INVITE_ID_LEN))?,
        enc: hkdf32(secret, labels::INVITE_ENC.as_bytes()),
        mac: hkdf32(secret, labels::INVITE_MAC.as_bytes()),
    })
}

fn labelled_ad(label: &str, id: &[u8]) -> Vec<u8> {
    [&labels::labelled(label)[..], id].concat()
}

/// A member's two public keys.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PublicIdentity {
    pub sign_pub: Vec<u8>,
    pub agree_pub: Vec<u8>,
}

/// Link invites only: the current epoch and its list key `K_e`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PackageKey {
    pub epoch: u64,
    pub list_key: Zeroizing<Vec<u8>>,
}

/// What the invitee learns from the link before anything else.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct InvitePackage {
    /// List id, lowercase hex: the genesis hash to verify the log from.
    pub list: String,
    /// Hash of the log head when the invite was made; for a link invite, of its `invite` entry.
    pub log_head: Vec<u8>,
    pub owner: PublicIdentity,
    pub name: String,
    /// The owner's account name when the invite was made, for the Join screen; empty: left out.
    pub owner_name: String,
    pub key: Option<PackageKey>,
}

const PACKAGE_VERSION: u64 = 1;

/// Canonical JSON: `{"v":1,"list":…,"log_head":…,"owner":…,"owner_agree":…,"name":…}`,
/// with `,"owner_name":…` after `name` unless it is empty, then
/// `,"epoch":…,"list_key":…` before the closing brace for a link invite.
pub fn encode_invite_package(p: &InvitePackage) -> Result<Zeroizing<Vec<u8>>> {
    require_len("owner sign key", &p.owner.sign_pub, KEY_LEN)?;
    require_len("owner agree key", &p.owner.agree_pub, KEY_LEN)?;
    let b = |x: &[u8]| go_json_string(&encode_base64url(x));
    let mut s = Zeroizing::new(format!(
        r#"{{"v":{PACKAGE_VERSION},"list":{},"log_head":{},"owner":{},"owner_agree":{},"name":{}"#,
        go_json_string(&p.list),
        b(&p.log_head),
        b(&p.owner.sign_pub),
        b(&p.owner.agree_pub),
        go_json_string(&p.name),
    ));
    if !p.owner_name.is_empty() {
        s.push_str(&format!(r#","owner_name":{}"#, go_json_string(&p.owner_name)));
    }
    if let Some(k) = &p.key {
        check_safe("epoch", k.epoch)?;
        require_len("list key", &k.list_key, KEY_LEN)?;
        s.push_str(&format!(r#","epoch":{},"list_key":{}"#, k.epoch, b(&k.list_key)));
    }
    s.push('}');
    Ok(Zeroizing::new(s.as_bytes().to_vec()))
}

pub fn parse_invite_package(raw: &[u8]) -> Result<InvitePackage> {
    let malformed = || CryptoError::new("malformed invite package");
    let o: Value = serde_json::from_slice(raw).map_err(|_| malformed())?;
    let o = o.as_object().ok_or_else(malformed)?;
    let s = |k: &str| o.get(k).and_then(Value::as_str).ok_or_else(malformed);
    let bytes = |k: &str| decode_base64url(s(k)?).map_err(|_| malformed());
    if o.get("v").and_then(Value::as_u64) != Some(PACKAGE_VERSION) {
        return Err(malformed());
    }
    let key = if o.contains_key("epoch") || o.contains_key("list_key") {
        let epoch = o.get("epoch").and_then(Value::as_u64).ok_or_else(malformed)?;
        Some(PackageKey { epoch, list_key: Zeroizing::new(bytes("list_key")?) })
    } else {
        None
    };
    let p = InvitePackage {
        list: s("list")?.to_owned(),
        log_head: bytes("log_head")?,
        owner: PublicIdentity { sign_pub: bytes("owner")?, agree_pub: bytes("owner_agree")? },
        name: s("name")?.to_owned(),
        owner_name: if o.contains_key("owner_name") { s("owner_name")?.to_owned() } else { String::new() },
        key,
    };
    if encode_invite_package(&p)?[..] != *raw {
        return Err(CryptoError::new("invite package not in canonical form"));
    }
    Ok(p)
}

/// `nonce ‖ AES-GCM(enc, package, AD = "…/invite-package" ‖ 0x00 ‖ id)`.
pub fn seal_invite_package(keys: &InviteKeys, p: &InvitePackage, nonce: Option<&[u8; GCM_NONCE]>) -> Result<Vec<u8>> {
    aes_gcm_seal_box(&keys.enc[..], &encode_invite_package(p)?, &labelled_ad(labels::INVITE_PACKAGE, &keys.id), nonce)
}

pub fn open_invite_package(keys: &InviteKeys, sealed: &[u8]) -> Result<InvitePackage> {
    parse_invite_package(&aes_gcm_open_box(&keys.enc[..], sealed, &labelled_ad(labels::INVITE_PACKAGE, &keys.id))?)
}

/// `nonce ‖ AES-GCM(enc, display name, AD = "…/invite-join" ‖ 0x00 ‖ id)`.
pub fn seal_join_name(keys: &InviteKeys, display_name: &str, nonce: Option<&[u8; GCM_NONCE]>) -> Result<Vec<u8>> {
    aes_gcm_seal_box(&keys.enc[..], display_name.as_bytes(), &labelled_ad(labels::INVITE_JOIN, &keys.id), nonce)
}

pub fn open_join_name(keys: &InviteKeys, sealed: &[u8]) -> Result<String> {
    let raw = aes_gcm_open_box(&keys.enc[..], sealed, &labelled_ad(labels::INVITE_JOIN, &keys.id))?;
    String::from_utf8(raw.to_vec()).map_err(|_| CryptoError::new("display name is not UTF-8"))
}

/// The link invite's one-time key pair (E2EE §6.1): the Ed25519 seed is
/// HKDF(s, "…/invite-sign"), so every holder of the link derives it.
pub fn derive_invite_sign_key(secret: &[u8]) -> Result<SigningKeyPair> {
    require_len("invite secret", secret, INVITE_SECRET_LEN)?;
    SigningKeyPair::from_seed(&hkdf32(secret, labels::INVITE_SIGN.as_bytes())[..])
}

/// `"…/join" ‖ 0x00 ‖ list_id(32) ‖ joiner sign_pub(32) ‖ joiner agree_pub(32)`.
pub fn join_proof_message(list_id: &[u8], joiner: &PublicIdentity) -> Result<Vec<u8>> {
    require_len("list id", list_id, 32)?;
    require_len("sign key", &joiner.sign_pub, KEY_LEN)?;
    require_len("agree key", &joiner.agree_pub, KEY_LEN)?;
    Ok([&labels::labelled(labels::JOIN)[..], list_id, &joiner.sign_pub, &joiner.agree_pub].concat())
}

/// A `join` entry's `invite_proof`: the invite key's signature over the join message.
pub fn sign_join_proof(invite_key: &SigningKeyPair, list_id: &[u8], joiner: &PublicIdentity) -> Result<[u8; SIG_LEN]> {
    Ok(invite_key.sign(&join_proof_message(list_id, joiner)?))
}

pub fn verify_join_proof(invite_pub: &[u8], list_id: &[u8], joiner: &PublicIdentity, proof: &[u8]) -> bool {
    join_proof_message(list_id, joiner).is_ok_and(|m| ed25519_verify(invite_pub, &m, proof))
}

fn proof_input(id: &[u8], joiner: &PublicIdentity) -> Result<Vec<u8>> {
    require_len("invite id", id, INVITE_ID_LEN)?;
    require_len("sign key", &joiner.sign_pub, KEY_LEN)?;
    require_len("agree key", &joiner.agree_pub, KEY_LEN)?;
    Ok([id, &joiner.sign_pub, &joiner.agree_pub].concat())
}

/// Approve-first (§6.2): the join request's MAC, later the `add` entry's
/// `invite_proof`: `HMAC(mac, id ‖ sign_pub ‖ agree_pub)`.
pub fn invite_proof(keys: &InviteKeys, joiner: &PublicIdentity) -> Result<[u8; 32]> {
    Ok(hmac_sha256(&keys.mac[..], &proof_input(&keys.id, joiner)?))
}

/// True when the join request's keys are exactly what the link holder sent.
pub fn verify_invite_proof(keys: &InviteKeys, joiner: &PublicIdentity, proof: &[u8]) -> Result<bool> {
    Ok(hmac_sha256_verify(&keys.mac[..], &proof_input(&keys.id, joiner)?, proof))
}
