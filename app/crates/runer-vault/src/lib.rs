//! The vault (client-spec §8): holds the keys and the verified membership
//! logs, applies the log rules, and hands the caller handles and verified
//! state, never key bytes. The same crate runs behind Tauri commands in the
//! app and as a napi-rs add-on in Node (`runer-vault-node`).

pub mod log;
pub mod storage;
mod vault;

pub use runer_crypto::account::ServerMessage;
pub use runer_crypto::content::SealedContent;
pub use vault::{
    AccountHandle, Applied, ContentKind, CreatedInvite, InviteHandle, InvitePreview, ListState, LogEvent, NewList,
    OpenInvite, Opened, PublicId, Rejected, Rekeyed, RotationDue, SignUp, StoredAccount, Vault, VaultError,
    VaultWarning,
};
