//! Thin wrappers over the primitive crates (E2EE §10). Nothing here chooses
//! a label or a layout; see `labels.rs` for that.

use aes_gcm::aead::{Aead as _, Payload};
use aes_gcm::{Aes256Gcm, KeyInit as _};
use ed25519_dalek::{Signer as _, Verifier as _};
use hmac::{Hmac, Mac as _};
use sha2::{Digest as _, Sha256};
use std::fmt;
use zeroize::Zeroizing;

pub const KEY_LEN: usize = 32;
pub const SIG_LEN: usize = 64;
pub const AES_KEY: usize = 32;
pub const GCM_NONCE: usize = 12;
pub const GCM_TAG: usize = 16;

/// Every failure in this crate. The message is for logs and tests; callers
/// treat any `CryptoError` as "reject".
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CryptoError(pub String);

impl CryptoError {
    pub fn new(msg: impl Into<String>) -> Self {
        Self(msg.into())
    }
}

impl fmt::Display for CryptoError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for CryptoError {}

pub type Result<T> = std::result::Result<T, CryptoError>;

pub fn require_len(name: &str, b: &[u8], len: usize) -> Result<()> {
    if b.len() == len { Ok(()) } else { Err(CryptoError::new(format!("{name}: want {len} bytes, got {}", b.len()))) }
}

/// `b` as a fixed-size array, or a length error naming `name`.
pub fn fixed<const N: usize>(name: &str, b: &[u8]) -> Result<[u8; N]> {
    require_len(name, b, N)?;
    Ok(b.try_into().expect("length checked"))
}

pub fn random_bytes<const N: usize>() -> [u8; N] {
    let mut out = [0u8; N];
    getrandom::fill(&mut out).expect("system RNG");
    out
}

/// Every counter in the protocol is a JavaScript-safe integer (E2EE §4.1);
/// holding Rust to the same bound keeps both sides' verdicts equal.
pub const MAX_SAFE_INTEGER: u64 = (1 << 53) - 1;

pub fn check_safe(name: &str, n: u64) -> Result<()> {
    if n <= MAX_SAFE_INTEGER { Ok(()) } else { Err(CryptoError::new(format!("{name} out of range"))) }
}

pub fn sha256(data: &[u8]) -> [u8; 32] {
    Sha256::digest(data).into()
}

/// HKDF-SHA-256 with an empty salt (RFC 5869: HashLen zero bytes).
pub fn hkdf(ikm: &[u8], info: &[u8], len: usize) -> Zeroizing<Vec<u8>> {
    let mut out = Zeroizing::new(vec![0u8; len]);
    hkdf::Hkdf::<Sha256>::new(None, ikm).expand(info, &mut out).expect("HKDF output length");
    out
}

pub fn hkdf32(ikm: &[u8], info: &[u8]) -> Zeroizing<[u8; 32]> {
    let out = hkdf(ikm, info, 32);
    Zeroizing::new(out[..].try_into().expect("32 bytes"))
}

pub fn hmac_sha256(key: &[u8], data: &[u8]) -> [u8; 32] {
    let mut m = Hmac::<Sha256>::new_from_slice(key).expect("HMAC takes any key length");
    m.update(data);
    m.finalize().into_bytes().into()
}

/// Constant-time HMAC check.
pub fn hmac_sha256_verify(key: &[u8], data: &[u8], mac: &[u8]) -> bool {
    let mut m = Hmac::<Sha256>::new_from_slice(key).expect("HMAC takes any key length");
    m.update(data);
    m.verify_slice(mac).is_ok()
}

fn aes(key: &[u8]) -> Result<Aes256Gcm> {
    require_len("AES key", key, AES_KEY)?;
    Ok(Aes256Gcm::new_from_slice(key).expect("length checked"))
}

/// AES-256-GCM. Returns `ciphertext ‖ tag`.
pub fn aes_gcm_seal(key: &[u8], nonce: &[u8], plaintext: &[u8], ad: &[u8]) -> Result<Vec<u8>> {
    require_len("GCM nonce", nonce, GCM_NONCE)?;
    aes(key)?
        .encrypt(nonce.try_into().expect("length checked"), Payload { msg: plaintext, aad: ad })
        .map_err(|_| CryptoError::new("encryption failed"))
}

/// Opens `ciphertext ‖ tag`.
pub fn aes_gcm_open(key: &[u8], nonce: &[u8], sealed: &[u8], ad: &[u8]) -> Result<Zeroizing<Vec<u8>>> {
    require_len("GCM nonce", nonce, GCM_NONCE)?;
    aes(key)?
        .decrypt(nonce.try_into().expect("length checked"), Payload { msg: sealed, aad: ad })
        .map(Zeroizing::new)
        .map_err(|_| CryptoError::new("decryption failed"))
}

/// `nonce ‖ ciphertext ‖ tag` with a fresh random nonce (or `nonce`, for vectors).
pub fn aes_gcm_seal_box(key: &[u8], plaintext: &[u8], ad: &[u8], nonce: Option<&[u8; GCM_NONCE]>) -> Result<Vec<u8>> {
    let nonce = nonce.copied().unwrap_or_else(random_bytes);
    Ok([&nonce[..], &aes_gcm_seal(key, &nonce, plaintext, ad)?].concat())
}

pub fn aes_gcm_open_box(key: &[u8], sealed: &[u8], ad: &[u8]) -> Result<Zeroizing<Vec<u8>>> {
    if sealed.len() < GCM_NONCE + GCM_TAG {
        return Err(CryptoError::new("ciphertext too short"));
    }
    let (nonce, ct) = sealed.split_at(GCM_NONCE);
    aes_gcm_open(key, nonce, ct, ad)
}

/// An Ed25519 key pair; the seed is the 32-byte private key (RFC 8032).
pub struct SigningKeyPair(ed25519_dalek::SigningKey);

impl SigningKeyPair {
    pub fn from_seed(seed: &[u8]) -> Result<Self> {
        let seed = Zeroizing::new(fixed::<KEY_LEN>("Ed25519 seed", seed)?);
        Ok(Self(ed25519_dalek::SigningKey::from_bytes(&seed)))
    }

    pub fn generate() -> Self {
        Self::from_seed(&Zeroizing::new(random_bytes::<KEY_LEN>())[..]).expect("32 bytes")
    }

    pub fn seed(&self) -> Zeroizing<[u8; KEY_LEN]> {
        Zeroizing::new(self.0.to_bytes())
    }

    pub fn public_key(&self) -> [u8; KEY_LEN] {
        self.0.verifying_key().to_bytes()
    }

    pub fn sign(&self, msg: &[u8]) -> [u8; SIG_LEN] {
        self.0.sign(msg).to_bytes()
    }
}

/// An X25519 key pair (RFC 7748).
pub struct AgreementKeyPair(x25519_dalek::StaticSecret);

impl AgreementKeyPair {
    pub fn from_private(private_key: &[u8]) -> Result<Self> {
        Ok(Self(x25519_dalek::StaticSecret::from(fixed::<KEY_LEN>("X25519 private key", private_key)?)))
    }

    pub fn generate() -> Self {
        Self::from_private(&Zeroizing::new(random_bytes::<KEY_LEN>())[..]).expect("32 bytes")
    }

    pub fn private_key(&self) -> Zeroizing<[u8; KEY_LEN]> {
        Zeroizing::new(self.0.to_bytes())
    }

    pub fn public_key(&self) -> [u8; KEY_LEN] {
        x25519_dalek::PublicKey::from(&self.0).to_bytes()
    }
}

/// Go's `ed25519.Verify` semantics as closely as `ed25519-dalek` allows:
/// cofactorless, rejects non-canonical S, accepts small-order keys (so not
/// `verify_strict`). Wrong lengths and invalid points are `false`, not errors.
pub fn ed25519_verify(public_key: &[u8], msg: &[u8], sig: &[u8]) -> bool {
    let (Ok(pk), Ok(sig)) = (fixed::<KEY_LEN>("pub", public_key), fixed::<SIG_LEN>("sig", sig)) else {
        return false;
    };
    let Ok(vk) = ed25519_dalek::VerifyingKey::from_bytes(&pk) else { return false };
    vk.verify(msg, &ed25519_dalek::Signature::from_bytes(&sig)).is_ok()
}
