use super::error::{LogError, LogErrorCode};
use super::wire::{WireEntry, encode_wire_entry, read_wire};
use runer_crypto::ed25519_verify;
use runer_crypto::encoding::decode_base64url_go;
use runer_crypto::primitives::{KEY_LEN, MAX_SAFE_INTEGER, SIG_LEN, sha256};

pub type Key = [u8; KEY_LEN];

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Envelope {
    pub recipient: Key,
    pub hpke_ciphertext: Vec<u8>,
}

/// A parsed, canonical, signature-verified log entry, plus the exact bytes
/// it was signed over. Every hash is computed from `raw`, never from a
/// re-serialisation (E2EE §4.1).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Entry {
    pub raw: Vec<u8>,
    pub sig: Vec<u8>,
    /// SHA-256(raw): the next entry's `prev`, and for a genesis entry the list id.
    pub hash: [u8; 32],
    pub group: String,
    pub seq: u64,
    pub prev: Vec<u8>,
    /// Not narrowed here: an unknown type is a validity failure, not a parse failure (as in Go).
    pub r#type: String,
    pub author: Key,
    pub author_agree: Option<Key>,
    pub subject: Option<Key>,
    pub subject_agree: Option<Key>,
    pub epoch: u64,
    pub envelopes: Vec<Envelope>,
    pub key_chain: Vec<u8>,
    /// A link invite's Ed25519 public key (invite, uninvite, join; E2EE §6.1).
    pub invite: Option<Key>,
    /// Unix milliseconds after which the invite can't be joined (invite only); 0 when absent.
    pub expires: u64,
    /// The approve-first MAC (add) or the invite key's signature (join).
    pub invite_proof: Vec<u8>,
    /// Unix milliseconds.
    pub ts: u64,
}

fn malformed(detail: impl Into<String>) -> LogError {
    LogError::malformed(detail)
}

/// Which fields each entry type carries (E2EE §4.2 rule 8).
fn check_shape(w: &WireEntry) -> Result<(), LogError> {
    let t = w.r#type.as_str();
    if t == "create" {
        if !w.group.is_empty() {
            return Err(malformed("create has no group; its id is its hash"));
        }
        if w.author_agree.is_empty() {
            return Err(malformed("create needs author_agree"));
        }
    } else if !w.author_agree.is_empty() {
        return Err(malformed("author_agree belongs to create only"));
    }
    let admits = t == "add" || t == "join";
    if admits == w.subject_agree.is_empty() {
        return Err(malformed("subject_agree is required on add and join and only there"));
    }

    let link_invite = matches!(t, "invite" | "uninvite" | "join");
    if link_invite == w.invite.is_empty() {
        return Err(malformed("invite is required on invite, uninvite and join and only there"));
    }
    if (t == "invite") == (w.expires == 0) {
        return Err(malformed("expires is required on invite and only there"));
    }
    if !w.invite_proof.is_empty() && !admits {
        return Err(malformed("invite_proof belongs to add and join only"));
    }
    if !link_invite {
        return Ok(());
    }
    if !w.envelopes.is_empty() || !w.key_chain.is_empty() {
        return Err(malformed(format!("{t} carries no envelopes or key_chain")));
    }
    if t == "join" {
        if w.subject.is_empty() || w.subject != w.author {
            return Err(malformed("join author must be the subject"));
        }
        if w.invite_proof.is_empty() {
            return Err(malformed("join needs invite_proof"));
        }
    } else if !w.subject.is_empty() {
        return Err(malformed(format!("{t} has no subject")));
    }
    Ok(())
}

fn decode_any(field: &str, s: &str) -> Result<Vec<u8>, LogError> {
    decode_base64url_go(s).map_err(|e| malformed(format!("{field}: {e}")))
}

fn decode_key(field: &str, s: &str) -> Result<Key, LogError> {
    let b = decode_any(field, s)?;
    let len = b.len();
    b.try_into().map_err(|_| malformed(format!("{field}: want {KEY_LEN} bytes, got {len}")))
}

fn decode_opt_key(field: &str, s: &str) -> Result<Option<Key>, LogError> {
    if s.is_empty() { Ok(None) } else { decode_key(field, s).map(Some) }
}

fn decode_opt(field: &str, s: &str) -> Result<Vec<u8>, LogError> {
    if s.is_empty() { Ok(Vec::new()) } else { decode_any(field, s) }
}

/// Decodes and signature-verifies a submitted entry and checks it is in
/// canonical form with the fields its type requires. Does not run the
/// state-dependent rules; see `validate_entry`. Mirrors Go's `grouplog.Parse`.
pub fn parse_entry(raw: &[u8], sig: &[u8]) -> Result<Entry, LogError> {
    let w = read_wire(raw)?;
    if raw != encode_wire_entry(&w) {
        return Err(malformed("not in canonical form"));
    }
    if w.v != 1 {
        return Err(malformed(format!("unsupported version {}", w.v)));
    }
    let safe = |n: i128| u64::try_from(n).ok().filter(|&n| n <= MAX_SAFE_INTEGER);
    let (Some(seq), Some(epoch), Some(expires), Some(ts)) = (safe(w.seq), safe(w.epoch), safe(w.expires), safe(w.ts))
    else {
        return Err(malformed("integer out of range"));
    };
    check_shape(&w)?;

    let author = decode_key("author", &w.author)?;
    if sig.len() != SIG_LEN || !ed25519_verify(&author, raw, sig) {
        return Err(LogError::new(LogErrorCode::InvalidSignature));
    }

    Ok(Entry {
        raw: raw.to_vec(),
        sig: sig.to_vec(),
        hash: sha256(raw),
        seq,
        prev: decode_opt("prev", &w.prev)?,
        author,
        author_agree: decode_opt_key("author_agree", &w.author_agree)?,
        subject: decode_opt_key("subject", &w.subject)?,
        subject_agree: decode_opt_key("subject_agree", &w.subject_agree)?,
        epoch,
        envelopes: w
            .envelopes
            .iter()
            .map(|e| {
                Ok(Envelope {
                    recipient: decode_key("envelope recipient", &e.recipient)?,
                    hpke_ciphertext: decode_any("envelope ciphertext", &e.hpke_ciphertext)?,
                })
            })
            .collect::<Result<_, LogError>>()?,
        key_chain: decode_opt("key_chain", &w.key_chain)?,
        invite: decode_opt_key("invite", &w.invite)?,
        expires,
        invite_proof: decode_opt("invite_proof", &w.invite_proof)?,
        ts,
        group: w.group,
        r#type: w.r#type,
    })
}
