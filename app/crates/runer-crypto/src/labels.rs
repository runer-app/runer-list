//! Every domain-separation string in the protocol, in one place.
//!
//! `PREFIX` is an internal namespace, never shown to users. Changing it
//! changes every derived key and signed message, so it can't change once real
//! accounts exist, and never without new vectors and the server's
//! login/rekey messages.
//!
//! Conventions (E2EE §10.1):
//! - HKDF: salt empty, `info` = the label bytes alone, output 32 bytes.
//! - Anything else built from a label: `label ‖ 0x00 ‖ fields`, with fixed-width
//!   raw fields (keys, hashes, ids) and integers as `uint64be`. Only a
//!   variable-length field (the login URL) gets a `uint16be` length prefix.

macro_rules! labels {
    ($prefix:literal; $($name:ident = $suffix:literal;)*) => {
        pub const PREFIX: &str = $prefix;
        $(pub const $name: &str = concat!($prefix, $suffix);)*
    };
}

labels! {
    // The one place the prefix is written.
    "runer/v1/";
    // HKDF info strings
    AUTH = "auth";
    WRAP = "wrap";
    INVITE_ID = "invite-id";
    INVITE_ENC = "invite-enc";
    INVITE_MAC = "invite-mac";
    INVITE_SIGN = "invite-sign";
    LIST = "list";
    CHAIN = "chain";
    // Both the HKDF info of the key for the account's settings and the label of its AD
    ACCOUNT_SETTINGS = "account-settings";
    // Signed messages (fixed by the server, internal/auth/messages.go)
    LOGIN = "login";
    REKEY = "rekey";
    IDENTITY = "identity";
    // Label ‖ 0x00 ‖ fields
    ENVELOPE = "env";
    SAFETY = "safety";
    INVITE_PACKAGE = "invite-package";
    INVITE_JOIN = "invite-join";
    JOIN = "join";
}

/// `label ‖ 0x00`, the start of every labelled input that isn't an HKDF info.
pub fn labelled(label: &str) -> Vec<u8> {
    let mut out = label.as_bytes().to_vec();
    out.push(0);
    out
}
