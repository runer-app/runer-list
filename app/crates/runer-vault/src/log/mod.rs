//! The membership log (E2EE §4): canonical encoding, parsing and the
//! validity rules, a port of the Go server's `grouplog` that must reach the
//! same verdict on every file in `protocol/log-fixtures`.

mod entry;
mod error;
mod validate;
mod wire;

pub use entry::{Entry, Envelope, Key, parse_entry};
pub use error::{LogError, LogErrorCode};
pub use validate::{
    InviteState, LogState, MAX_INVITE_LIFETIME_MS, Member, Role, SignedEntry, apply_entries, validate_entry,
};
pub use wire::{WireEntry, WireEnvelope, encode_wire_entry};
