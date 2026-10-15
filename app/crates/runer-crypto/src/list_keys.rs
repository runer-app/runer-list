//! List keys, key chain and HPKE envelopes (E2EE §5).

use crate::encoding::decode_hex_lower;
use crate::labels;
use crate::primitives::{
    CryptoError, GCM_NONCE, KEY_LEN, Result, aes_gcm_open, aes_gcm_seal, check_safe, fixed, hkdf32, random_bytes,
    require_len,
};
use hpke::aead::AesGcm256;
use hpke::kdf::HkdfSha256;
use hpke::kem::X25519HkdfSha256;
use hpke::{Deserializable as _, OpModeR, OpModeS, Serializable as _};
use zeroize::Zeroizing;

type Kem = X25519HkdfSha256;

/// A list key `K_e`: 32 random bytes, only ever used as HKDF input.
pub fn generate_list_key() -> Zeroizing<[u8; KEY_LEN]> {
    Zeroizing::new(random_bytes())
}

/// Decodes a list id (`hex(SHA-256(genesis))`) to its 32 raw bytes.
pub fn list_id_bytes(list_id: &str) -> Result<[u8; 32]> {
    decode_hex_lower(list_id)
        .filter(|b| b.len() == 32)
        .map(|b| b.try_into().expect("32 bytes"))
        .ok_or_else(|| CryptoError::new("list id must be 64 lowercase hex characters"))
}

/// The AES-256-GCM key for the list document at this epoch.
pub fn document_key(list_key: &[u8]) -> Result<Zeroizing<[u8; 32]>> {
    require_len("list key", list_key, KEY_LEN)?;
    Ok(hkdf32(list_key, labels::LIST.as_bytes()))
}

fn chain_key(list_key: &[u8]) -> Result<Zeroizing<[u8; 32]>> {
    require_len("list key", list_key, KEY_LEN)?;
    Ok(hkdf32(list_key, labels::CHAIN.as_bytes()))
}

// K_chain(e) encrypts exactly one message, so a fixed nonce is safe.
const ZERO_NONCE: [u8; GCM_NONCE] = [0; GCM_NONCE];

/// `key_chain` for the entry that starts epoch e: `AES-GCM(K_chain(e), 0¹², K_{e-1})`, 48 bytes.
pub fn seal_key_chain(new_key: &[u8], previous_key: &[u8]) -> Result<Vec<u8>> {
    require_len("list key", previous_key, KEY_LEN)?;
    aes_gcm_seal(&chain_key(new_key)?[..], &ZERO_NONCE, previous_key, &[])
}

/// Recovers `K_{e-1}` from `K_e` and the `key_chain` of the entry that started epoch e.
pub fn open_key_chain(new_key: &[u8], key_chain: &[u8]) -> Result<Zeroizing<[u8; KEY_LEN]>> {
    let prev = aes_gcm_open(&chain_key(new_key)?[..], &ZERO_NONCE, key_chain, &[])?;
    Ok(Zeroizing::new(fixed("chained list key", &prev)?))
}

/// The list id in the HPKE info of the genesis entry's envelope: the real id
/// is the hash of that entry, so it can't be inside it (E2EE §5). The
/// envelope is still bound to the list, because it is part of those bytes.
pub const GENESIS_LIST_ID: [u8; 32] = [0; 32];

/// Size of the KEM output (`enc`) at the front of `hpke_ciphertext`.
const HPKE_ENC: usize = 32;

/// `"…/env" ‖ 0x00 ‖ list_id(32) ‖ uint64be(epoch) ‖ recipient identity_sign_pub(32)`.
pub fn envelope_info(list_id: &[u8], epoch: u64, recipient_sign_pub: &[u8]) -> Result<Vec<u8>> {
    let list_id = fixed::<32>("list id", list_id)?;
    let recipient = fixed::<KEY_LEN>("recipient", recipient_sign_pub)?;
    check_safe("epoch", epoch)?;
    Ok([&labels::labelled(labels::ENVELOPE)[..], &list_id, &epoch.to_be_bytes(), &recipient].concat())
}

pub struct EnvelopeTarget<'a> {
    /// The member's `identity_sign_pub`: the envelope's `recipient` field.
    pub sign_pub: &'a [u8],
    /// The member's `identity_agree_pub`, as recorded in the verified log.
    pub agree_pub: &'a [u8],
}

/// Wraps `list_key` for one member: HPKE base mode, empty AAD, and
/// `hpke_ciphertext = enc(32) ‖ ciphertext ‖ tag` (80 bytes).
pub fn seal_envelope(list_id: &[u8], epoch: u64, to: &EnvelopeTarget<'_>, list_key: &[u8]) -> Result<Vec<u8>> {
    require_len("list key", list_key, KEY_LEN)?;
    require_len("recipient agree key", to.agree_pub, KEY_LEN)?;
    let pk = <Kem as hpke::Kem>::PublicKey::from_bytes(to.agree_pub)
        .map_err(|_| CryptoError::new("bad recipient agree key"))?;
    let info = envelope_info(list_id, epoch, to.sign_pub)?;
    let (enc, ct) = hpke::single_shot_seal::<AesGcm256, HkdfSha256, Kem>(&OpModeS::Base, &pk, &info, list_key, &[])
        .map_err(|_| CryptoError::new("cannot seal envelope"))?;
    Ok([&enc.to_bytes()[..], &ct].concat())
}

/// Opens the envelope addressed to us; fails if it isn't ours or was tampered with.
pub fn open_envelope(
    list_id: &[u8],
    epoch: u64,
    my_sign_pub: &[u8],
    my_agree_private: &[u8],
    hpke_ciphertext: &[u8],
) -> Result<Zeroizing<[u8; KEY_LEN]>> {
    if hpke_ciphertext.len() <= HPKE_ENC {
        return Err(CryptoError::new("envelope too short"));
    }
    let info = envelope_info(list_id, epoch, my_sign_pub)?;
    let cannot = || CryptoError::new("cannot open envelope");
    let sk = <Kem as hpke::Kem>::PrivateKey::from_bytes(my_agree_private).map_err(|_| cannot())?;
    let (enc, ct) = hpke_ciphertext.split_at(HPKE_ENC);
    let enc = <Kem as hpke::Kem>::EncappedKey::from_bytes(enc).map_err(|_| cannot())?;
    let key = Zeroizing::new(
        hpke::single_shot_open::<AesGcm256, HkdfSha256, Kem>(&OpModeR::Base, &sk, &enc, &info, ct, &[])
            .map_err(|_| cannot())?,
    );
    Ok(Zeroizing::new(fixed("enveloped list key", &key)?))
}
