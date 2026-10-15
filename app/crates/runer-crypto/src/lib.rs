//! All of the client's cryptography (client-spec §8): account, identity,
//! list keys, envelopes, content, invites and the safety code, following
//! E2EE §3, §5, §6, §7 and the byte conventions in §10.1. Checked against
//! the shared vectors in `protocol/vectors`.
//!
//! This layer works on bytes. What leaves Rust (handles, batching, which
//! messages may be signed) is decided by the bindings, not here.

pub mod account;
pub mod content;
pub mod encoding;
pub mod invite;
pub mod labels;
pub mod list_keys;
pub mod primitives;
pub mod safety;

pub use primitives::{AgreementKeyPair, CryptoError, Result, SigningKeyPair, ed25519_verify};
