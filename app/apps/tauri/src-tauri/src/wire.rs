//! What crosses the IPC boundary: bytes travel as unpadded base64url strings
//! (a JSON array of numbers would be four times the size and slow to parse),
//! and every failure is a `{ code, message }` the TypeScript side rethrows.

use runer_crypto::encoding::{decode_base64url, encode_base64url};
use serde::{Deserialize, Deserializer, Serialize, Serializer};

/// Bytes as base64url on the wire.
#[derive(Debug, Clone, Default)]
pub struct B64(pub Vec<u8>);

impl Serialize for B64 {
    fn serialize<S: Serializer>(&self, s: S) -> std::result::Result<S::Ok, S::Error> {
        s.serialize_str(&encode_base64url(&self.0))
    }
}

impl<'de> Deserialize<'de> for B64 {
    fn deserialize<D: Deserializer<'de>>(d: D) -> std::result::Result<Self, D::Error> {
        let s = String::deserialize(d)?;
        decode_base64url(&s).map(B64).map_err(<D::Error as serde::de::Error>::custom)
    }
}

impl From<&[u8]> for B64 {
    fn from(b: &[u8]) -> Self {
        Self(b.to_vec())
    }
}

impl From<Vec<u8>> for B64 {
    fn from(b: Vec<u8>) -> Self {
        Self(b)
    }
}

/// A command's failure. `code` is the vault's error code for vault calls
/// (a log rule's name for a rejected log), and a short word otherwise.
#[derive(Debug, Serialize)]
pub struct Error {
    pub code: String,
    pub message: String,
}

impl Error {
    pub fn new(code: &str, message: impl Into<String>) -> Self {
        Self { code: code.to_owned(), message: message.into() }
    }
}

impl From<runer_vault::VaultError> for Error {
    fn from(e: runer_vault::VaultError) -> Self {
        Self::new(e.code(), e.to_string())
    }
}

pub type Result<T> = std::result::Result<T, Error>;
