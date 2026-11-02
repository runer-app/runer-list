//! Encrypted, signed document updates and snapshots (E2EE §7).

use crate::encoding::{decode_base64url, encode_base64url, go_json_string};
use crate::primitives::{
    CryptoError, GCM_NONCE, GCM_TAG, Result, SigningKeyPair, aes_gcm_open, aes_gcm_seal, check_safe, ed25519_verify,
    random_bytes,
};
use serde_json::Value;
use zeroize::Zeroizing;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ContentType {
    Update,
    Snapshot,
}

impl ContentType {
    fn as_str(self) -> &'static str {
        match self {
            Self::Update => "update",
            Self::Snapshot => "snapshot",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ContentHeader {
    pub content_type: ContentType,
    /// List id, lowercase hex.
    pub list: String,
    pub epoch: u64,
    /// Snapshots only: the highest update `seq` this snapshot covers.
    pub covers_seq: Option<u64>,
    /// Author's `identity_sign_pub`.
    pub author: Vec<u8>,
    /// Hash of the newest log entry the author had verified (the next entry's `prev`).
    pub log_head: Vec<u8>,
}

/// Canonical header bytes: keys in the order `type, list, epoch, covers_seq,
/// author, log_head`, no whitespace, `covers_seq` only on snapshots. Matches
/// the server's `updateHeader`, which always writes `log_head`.
pub fn encode_content_header(h: &ContentHeader) -> Result<Vec<u8>> {
    check_safe("epoch", h.epoch)?;
    if (h.content_type == ContentType::Snapshot) != h.covers_seq.is_some() {
        return Err(CryptoError::new("covers_seq belongs on snapshots, and only there"));
    }
    let mut s = format!(
        r#"{{"type":{},"list":{},"epoch":{}"#,
        go_json_string(h.content_type.as_str()),
        go_json_string(&h.list),
        h.epoch
    );
    if let Some(c) = h.covers_seq {
        check_safe("covers_seq", c)?;
        s += &format!(r#","covers_seq":{c}"#);
    }
    s += &format!(
        r#","author":{},"log_head":{}}}"#,
        go_json_string(&encode_base64url(&h.author)),
        go_json_string(&encode_base64url(&h.log_head))
    );
    Ok(s.into_bytes())
}

/// Parses header bytes and rejects anything that isn't their canonical encoding.
pub fn parse_content_header(raw: &[u8]) -> Result<ContentHeader> {
    let malformed = || CryptoError::new("malformed header");
    let o: Value = serde_json::from_slice(raw).map_err(|_| malformed())?;
    let o = o.as_object().ok_or_else(malformed)?;
    let str_of = |k: &str| o.get(k).and_then(Value::as_str).ok_or_else(malformed);
    let content_type = match str_of("type")? {
        "update" => ContentType::Update,
        "snapshot" => ContentType::Snapshot,
        _ => return Err(malformed()),
    };
    let covers_seq = match o.get("covers_seq") {
        None => None,
        Some(v) => Some(v.as_u64().ok_or_else(malformed)?),
    };
    let h = ContentHeader {
        content_type,
        list: str_of("list")?.to_owned(),
        epoch: o.get("epoch").and_then(Value::as_u64).ok_or_else(malformed)?,
        covers_seq,
        author: decode_base64url(str_of("author")?).map_err(|_| malformed())?,
        log_head: decode_base64url(str_of("log_head")?).map_err(|_| malformed())?,
    };
    if encode_content_header(&h).map_err(|_| malformed())? != raw {
        return Err(CryptoError::new("header not in canonical form"));
    }
    Ok(h)
}

/// What travels (as base64url strings) in `update` and `snapshot` frames.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SealedContent {
    pub header: Vec<u8>,
    pub nonce: Vec<u8>,
    pub ct: Vec<u8>,
    pub sig: Vec<u8>,
}

/// `ct = AES-GCM(document_key, nonce, body, AD = header)`,
/// `sig = Ed25519(author, header ‖ nonce ‖ ct)`. `nonce` is for vectors only.
pub fn seal_content(
    doc_key: &[u8],
    author: &SigningKeyPair,
    header: &ContentHeader,
    body: &[u8],
    nonce: Option<&[u8; GCM_NONCE]>,
) -> Result<SealedContent> {
    let raw = encode_content_header(header)?;
    let nonce = nonce.copied().unwrap_or_else(random_bytes);
    let ct = aes_gcm_seal(doc_key, &nonce, body, &raw)?;
    let sig = author.sign(&[&raw[..], &nonce, &ct].concat());
    Ok(SealedContent { header: raw, nonce: nonce.to_vec(), ct, sig: sig.to_vec() })
}

/// Verifies the author's signature, then decrypts. The caller must first
/// check that `header.author` was a member at `header.epoch` and pick the
/// document key for that epoch.
pub fn open_content(doc_key: &[u8], msg: &SealedContent) -> Result<(ContentHeader, Zeroizing<Vec<u8>>)> {
    let header = parse_content_header(&msg.header)?;
    if msg.nonce.len() != GCM_NONCE || msg.ct.len() < GCM_TAG {
        return Err(CryptoError::new("malformed content"));
    }
    if !ed25519_verify(&header.author, &[&msg.header[..], &msg.nonce, &msg.ct].concat(), &msg.sig) {
        return Err(CryptoError::new("bad content signature"));
    }
    let body = aes_gcm_open(doc_key, &msg.nonce, &msg.ct, &msg.header)?;
    Ok((header, body))
}
