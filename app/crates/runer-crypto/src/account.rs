//! Account Key, auth key, wrap key, signed server messages and the Identity
//! Bundle (E2EE §3).

use crate::labels;
use crate::primitives::{
    AgreementKeyPair, CryptoError, GCM_NONCE, KEY_LEN, Result, SigningKeyPair, aes_gcm_open_box, aes_gcm_seal_box,
    check_safe, fixed, hkdf32, random_bytes,
};
use zeroize::Zeroizing;

/// 128 bits: 12 BIP-39 words.
pub const ACCOUNT_KEY_LEN: usize = 16;

pub type AccountKey = Zeroizing<[u8; ACCOUNT_KEY_LEN]>;

pub fn generate_account_key() -> AccountKey {
    Zeroizing::new(random_bytes())
}

pub fn account_key_to_words(account_key: &[u8]) -> Result<Zeroizing<Vec<String>>> {
    let k = Zeroizing::new(fixed::<ACCOUNT_KEY_LEN>("Account Key", account_key)?);
    let m = bip39::Mnemonic::from_entropy(&k[..]).map_err(|_| CryptoError::new("bad entropy"))?;
    Ok(Zeroizing::new(m.words().map(str::to_owned).collect()))
}

/// Parses typed words (any case, any whitespace). Returns `None` for a wrong
/// word count, an unknown word or a bad checksum, so typos are caught before
/// any request.
pub fn words_to_account_key(input: &str) -> Option<AccountKey> {
    let words: Vec<String> = input.split_whitespace().map(str::to_lowercase).collect();
    if words.len() != 12 {
        return None;
    }
    let phrase = Zeroizing::new(words.join(" "));
    let m = bip39::Mnemonic::parse_in_normalized(bip39::Language::English, &phrase).ok()?;
    let entropy = Zeroizing::new(m.to_entropy());
    Some(Zeroizing::new(entropy[..].try_into().ok()?))
}

fn check_account_key(k: &[u8]) -> Result<()> {
    fixed::<ACCOUNT_KEY_LEN>("Account Key", k).map(|_| ())
}

/// The login key pair: HKDF(Account Key, "…/auth") is the Ed25519 seed.
pub fn derive_auth_key(account_key: &[u8]) -> Result<SigningKeyPair> {
    check_account_key(account_key)?;
    SigningKeyPair::from_seed(&hkdf32(account_key, labels::AUTH.as_bytes())[..])
}

/// The AES-256-GCM key that encrypts the Identity Bundle.
pub fn derive_wrap_key(account_key: &[u8]) -> Result<Zeroizing<[u8; 32]>> {
    check_account_key(account_key)?;
    Ok(hkdf32(account_key, labels::WRAP.as_bytes()))
}

/// The three messages the server asks the auth key to sign.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ServerMessage {
    Login,
    Rekey,
    Identity,
}

impl ServerMessage {
    fn label(self) -> &'static str {
        match self {
            Self::Login => labels::LOGIN,
            Self::Rekey => labels::REKEY,
            Self::Identity => labels::IDENTITY,
        }
    }

    /// `label ‖ 0x00 ‖ uint16be(len(url)) ‖ url ‖ nonce`, as in the server's
    /// `buildMessage`. `server_url` is the base URL the client connected to,
    /// never one the server supplied.
    pub fn bytes(self, server_url: &str, nonce: &[u8]) -> Result<Vec<u8>> {
        let url = server_url.as_bytes();
        let len = u16::try_from(url.len()).map_err(|_| CryptoError::new("server URL too long"))?;
        Ok([&labels::labelled(self.label())[..], &len.to_be_bytes(), url, nonce].concat())
    }
}

/// The long-lived identity: random at signup, never derived from the Account Key.
pub struct Identity {
    pub sign: SigningKeyPair,
    pub agree: AgreementKeyPair,
}

impl Identity {
    pub fn generate() -> Self {
        Self { sign: SigningKeyPair::generate(), agree: AgreementKeyPair::generate() }
    }

    pub fn from_secrets(sign_seed: &[u8], agree_private: &[u8]) -> Result<Self> {
        Ok(Self { sign: SigningKeyPair::from_seed(sign_seed)?, agree: AgreementKeyPair::from_private(agree_private)? })
    }
}

const BUNDLE_VERSION: u8 = 1;

/// AD = `auth_pub ‖ uint64be(bundle_gen)` (E2EE §3.3).
fn bundle_ad(auth_pub: &[u8], bundle_gen: u64) -> Result<Vec<u8>> {
    check_safe("bundle_gen", bundle_gen)?;
    Ok([&fixed::<KEY_LEN>("auth_pub", auth_pub)?[..], &bundle_gen.to_be_bytes()].concat())
}

/// Identity Bundle: `nonce ‖ AES-GCM(wrap, 0x01 ‖ sign_seed ‖ agree_private)`.
/// `nonce` is for vectors only.
pub fn seal_identity_bundle(
    wrap_key: &[u8],
    auth_pub: &[u8],
    bundle_gen: u64,
    identity: &Identity,
    nonce: Option<&[u8; GCM_NONCE]>,
) -> Result<Vec<u8>> {
    let plaintext =
        Zeroizing::new([&[BUNDLE_VERSION][..], &identity.sign.seed()[..], &identity.agree.private_key()[..]].concat());
    aes_gcm_seal_box(wrap_key, &plaintext, &bundle_ad(auth_pub, bundle_gen)?, nonce)
}

pub fn open_identity_bundle(wrap_key: &[u8], auth_pub: &[u8], bundle_gen: u64, bundle: &[u8]) -> Result<Identity> {
    let p = aes_gcm_open_box(wrap_key, bundle, &bundle_ad(auth_pub, bundle_gen)?)?;
    if p.len() != 1 + 2 * KEY_LEN || p[0] != BUNDLE_VERSION {
        return Err(CryptoError::new("unknown identity bundle format"));
    }
    Identity::from_secrets(&p[1..1 + KEY_LEN], &p[1 + KEY_LEN..])
}

/// The key for something the account keeps on the server for itself (its
/// settings): it follows the identity, not the Account Key, so a
/// change of account key leaves it readable.
fn own_key(identity: &Identity, label: &str) -> Zeroizing<[u8; 32]> {
    hkdf32(&identity.agree.private_key()[..], label.as_bytes())
}

/// AD = `label ‖ 0x00 ‖ identity_sign_pub`.
fn own_ad(identity: &Identity, label: &str) -> Vec<u8> {
    [&labels::labelled(label)[..], &identity.sign.public_key()].concat()
}

/// `nonce ‖ AES-GCM(HKDF(identity_agree_private, label), UTF-8 text)`.
fn seal_own(identity: &Identity, label: &str, text: &str, nonce: Option<&[u8; GCM_NONCE]>) -> Result<Vec<u8>> {
    aes_gcm_seal_box(&own_key(identity, label)[..], text.as_bytes(), &own_ad(identity, label), nonce)
}

fn open_own(identity: &Identity, label: &str, sealed: &[u8], not_utf8: &'static str) -> Result<String> {
    let raw = aes_gcm_open_box(&own_key(identity, label)[..], sealed, &own_ad(identity, label))?;
    String::from_utf8(raw.to_vec()).map_err(|_| CryptoError::new(not_utf8))
}

/// The account's settings as the server keeps them (`settings_sealed`):
/// `nonce ‖ AES-GCM(HKDF(identity_agree_private, "…/account-settings"), UTF-8 JSON)`,
/// AD = `"…/account-settings" ‖ 0x00 ‖ identity_sign_pub`. What the JSON holds is the caller's business. `nonce` is for tests only.
pub fn seal_account_settings(identity: &Identity, json: &str, nonce: Option<&[u8; GCM_NONCE]>) -> Result<Vec<u8>> {
    seal_own(identity, labels::ACCOUNT_SETTINGS, json, nonce)
}

pub fn open_account_settings(identity: &Identity, sealed: &[u8]) -> Result<String> {
    open_own(identity, labels::ACCOUNT_SETTINGS, sealed, "account settings are not UTF-8")
}
