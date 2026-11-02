//! The `vault` commands: `runer_vault::Vault` behind IPC, the same mapping
//! as the Node add-on (`crates/runer-vault-node`). Handles are strings and
//! bytes are base64url; no command takes or returns key bytes, except the
//! words on the two screens that must show them and an
//! invite link's secret, which is in the link by nature (E2EE §6.1).
//!
//! Every command is async, so it runs off the main thread (the sign-up
//! proof-of-work takes a moment).

use crate::wire::{B64, Error, Result};
use runer_vault::log::SignedEntry as RsSignedEntry;
use runer_vault::{
    ContentKind, ListState as RsListState, LogEvent, PublicId, SealedContent as RsSealedContent, ServerMessage,
    StoredAccount as RsStoredAccount, Vault, VaultWarning,
};
use serde::{Deserialize, Serialize};
use std::sync::{Mutex, MutexGuard, PoisonError};
use tauri::State;

/// The vault, or why it couldn't open (no keychain, an unreadable store).
pub struct VaultState(Mutex<std::result::Result<Vault, String>>);

impl VaultState {
    pub fn new(vault: std::result::Result<Vault, String>) -> Self {
        Self(Mutex::new(vault))
    }

    fn lock(&self) -> MutexGuard<'_, std::result::Result<Vault, String>> {
        self.0.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn with<T>(&self, f: impl FnOnce(&mut Vault) -> Result<T>) -> Result<T> {
        match &mut *self.lock() {
            Ok(v) => f(v),
            Err(why) => Err(Error::new("Storage", why.clone())),
        }
    }
}

#[derive(Serialize, Deserialize)]
pub struct SignedEntry {
    raw: B64,
    sig: B64,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PublicIdentity {
    sign_pub: B64,
    agree_pub: B64,
}

#[derive(Serialize)]
pub struct NewList {
    list: String,
    entry: SignedEntry,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SignIn {
    account: String,
    auth_pub: B64,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SignUp {
    account: String,
    auth_pub: B64,
    identity: PublicIdentity,
    bundle: B64,
    inbox: NewList,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Rekeyed {
    auth_pub: B64,
    bundle: B64,
}

#[derive(Serialize)]
pub struct LoadedIdentity {
    identity: PublicIdentity,
    warnings: Vec<&'static str>,
}

#[derive(Serialize)]
pub struct Head {
    seq: u64,
    hash: B64,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct OpenInvite {
    invite_pub: B64,
    expires: u64,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LinkJoin {
    member: B64,
    invite_pub: B64,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ListState {
    id: String,
    owner: PublicIdentity,
    members: Vec<PublicIdentity>,
    epoch: u64,
    head: Head,
    open_invites: Vec<OpenInvite>,
    link_joins: Vec<LinkJoin>,
    created: u64,
    /// `member-left` or `invite-revoked`.
    rotation_due: Option<&'static str>,
}

/// `kind` is added, joined, removed, left (with `member`) or rotated (with `epoch`).
#[derive(Serialize)]
pub struct Event {
    kind: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    member: Option<B64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    epoch: Option<u64>,
}

#[derive(Serialize)]
pub struct Applied {
    state: ListState,
    events: Vec<Event>,
    warnings: Vec<&'static str>,
}

#[derive(Serialize, Deserialize)]
pub struct SealedContent {
    header: B64,
    nonce: B64,
    ct: B64,
    sig: B64,
}

/// The body and its header's fields, or `rejected` with the reason.
#[derive(Serialize)]
#[serde(rename_all = "camelCase", untagged)]
pub enum OpenedContent {
    #[serde(rename_all = "camelCase")]
    Opened {
        body: B64,
        epoch: u64,
        author: B64,
        covers_seq: Option<u64>,
        head_known: bool,
    },
    Rejected {
        rejected: &'static str,
    },
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct StoredAccount {
    account: String,
    server: String,
    auth_pub: B64,
    identity: PublicIdentity,
    bundle_gen: u64,
    inbox: Option<String>,
}

fn signed(e: RsSignedEntry) -> SignedEntry {
    SignedEntry { raw: e.raw.into(), sig: e.sig.into() }
}

fn new_list(l: runer_vault::NewList) -> NewList {
    NewList { list: l.id, entry: signed(l.entry) }
}

fn identity(id: &PublicId) -> PublicIdentity {
    PublicIdentity { sign_pub: id.sign_pub[..].into(), agree_pub: id.agree_pub[..].into() }
}

fn list_state(s: RsListState) -> ListState {
    ListState {
        owner: identity(&s.owner),
        members: s.members.iter().map(identity).collect(),
        epoch: s.epoch,
        head: Head { seq: s.head_seq, hash: s.head_hash[..].into() },
        open_invites: s
            .open_invites
            .iter()
            .map(|i| OpenInvite { invite_pub: i.invite_pub[..].into(), expires: i.expires })
            .collect(),
        link_joins: s
            .link_joins
            .iter()
            .map(|j| LinkJoin { member: j.member[..].into(), invite_pub: j.invite_pub[..].into() })
            .collect(),
        created: s.created,
        rotation_due: s.rotation_due.map(|r| r.as_str()),
        id: s.id,
    }
}

fn event(e: &LogEvent) -> Event {
    let member = |kind, k: &[u8]| Event { kind, member: Some(k.into()), epoch: None };
    match e {
        LogEvent::Added(k) => member("added", k),
        LogEvent::Joined(k) => member("joined", k),
        LogEvent::Removed(k) => member("removed", k),
        LogEvent::Left(k) => member("left", k),
        LogEvent::Rotated(epoch) => Event { kind: "rotated", member: None, epoch: Some(*epoch) },
    }
}

fn warning(w: &VaultWarning) -> &'static str {
    match w {
        VaultWarning::LogFork => "log-fork",
        VaultWarning::LogRollback => "log-rollback",
        VaultWarning::BundleRollback => "bundle-rollback",
    }
}

fn stored_account(a: RsStoredAccount) -> StoredAccount {
    StoredAccount {
        auth_pub: a.auth_pub[..].into(),
        identity: identity(&a.identity),
        bundle_gen: a.bundle_gen,
        account: a.account,
        server: a.server,
        inbox: a.inbox,
    }
}

type S<'a> = State<'a, VaultState>;

#[tauri::command]
pub async fn vault_accounts(vault: S<'_>) -> Result<Vec<StoredAccount>> {
    vault.with(|v| Ok(v.accounts().into_iter().map(stored_account).collect()))
}

#[tauri::command]
pub async fn vault_set_inbox(vault: S<'_>, account: String, list: String) -> Result<()> {
    vault.with(|v| Ok(v.set_inbox(&account, &list)?))
}

#[tauri::command]
pub async fn vault_sign_in(vault: S<'_>, server: String, words: String) -> Result<Option<SignIn>> {
    vault.with(|v| {
        Ok(v.sign_in(&server, &words).map(|(account, auth_pub)| SignIn { account, auth_pub: auth_pub[..].into() }))
    })
}

#[tauri::command]
pub async fn vault_start_sign_up(vault: S<'_>, server: String) -> Result<SignUp> {
    vault.with(|v| {
        let s = v.start_sign_up(&server);
        Ok(SignUp {
            account: s.account,
            auth_pub: s.auth_pub[..].into(),
            identity: identity(&s.identity),
            bundle: s.bundle.into(),
            inbox: new_list(s.inbox),
        })
    })
}

#[tauri::command]
pub async fn vault_solve_sign_up_pow(vault: S<'_>, account: String, nonce: B64, bits: u32) -> Result<B64> {
    vault.with(|v| Ok(v.solve_sign_up_pow(&account, &nonce.0, bits)?.into()))
}

#[tauri::command]
pub async fn vault_confirm_sign_up(vault: S<'_>, account: String) -> Result<Vec<String>> {
    vault.with(|v| Ok(v.confirm_sign_up(&account)?))
}

#[tauri::command]
pub async fn vault_sign_out(vault: S<'_>, account: String) -> Result<()> {
    vault.with(|v| Ok(v.sign_out(&account)?))
}

#[tauri::command]
pub async fn vault_show_words(vault: S<'_>, account: String) -> Result<Vec<String>> {
    vault.with(|v| Ok(v.show_words(&account)?))
}

#[tauri::command]
pub async fn vault_seal_account_settings(vault: S<'_>, account: String, json: String) -> Result<B64> {
    vault.with(|v| Ok(B64(v.seal_account_settings(&account, &json)?)))
}

#[tauri::command]
pub async fn vault_open_account_settings(vault: S<'_>, account: String, sealed: B64) -> Result<String> {
    vault.with(|v| Ok(v.open_account_settings(&account, &sealed.0)?))
}

/// `kind` is login, rekey or identity.
#[tauri::command]
pub async fn vault_sign_server_message(vault: S<'_>, account: String, kind: String, nonce: B64) -> Result<B64> {
    let kind = match kind.as_str() {
        "login" => ServerMessage::Login,
        "rekey" => ServerMessage::Rekey,
        "identity" => ServerMessage::Identity,
        _ => return Err(Error::new("InvalidArgument", format!("unknown server message {kind}"))),
    };
    vault.with(|v| Ok(v.sign_server_message(&account, kind, &nonce.0)?.into()))
}

#[tauri::command]
pub async fn vault_load_identity(
    vault: S<'_>,
    account: String,
    bundle_gen: u64,
    bundle: B64,
) -> Result<LoadedIdentity> {
    vault.with(|v| {
        let (id, warnings) = v.load_identity(&account, bundle_gen, &bundle.0)?;
        Ok(LoadedIdentity { identity: identity(&id), warnings: warnings.iter().map(warning).collect() })
    })
}

#[tauri::command]
pub async fn vault_change_account_key(vault: S<'_>, account: String) -> Result<Rekeyed> {
    vault.with(|v| {
        let r = v.change_account_key(&account)?;
        Ok(Rekeyed { auth_pub: r.auth_pub[..].into(), bundle: r.bundle.into() })
    })
}

#[tauri::command]
pub async fn vault_commit_account_key(vault: S<'_>, account: String) -> Result<Vec<String>> {
    vault.with(|v| Ok(v.commit_account_key(&account)?))
}

#[tauri::command]
pub async fn vault_apply_log(
    vault: S<'_>,
    account: String,
    list: String,
    entries: Vec<SignedEntry>,
) -> Result<Applied> {
    let entries: Vec<RsSignedEntry> =
        entries.into_iter().map(|e| RsSignedEntry { raw: e.raw.0, sig: e.sig.0 }).collect();
    vault.with(|v| {
        let a = v.apply_log(&account, &list, &entries)?;
        Ok(Applied {
            state: list_state(a.state),
            events: a.events.iter().map(event).collect(),
            warnings: a.warnings.iter().map(warning).collect(),
        })
    })
}

#[tauri::command]
pub async fn vault_list_state(vault: S<'_>, account: String, list: String) -> Result<ListState> {
    vault.with(|v| Ok(list_state(v.list_state(&account, &list)?)))
}

#[tauri::command]
pub async fn vault_check_head(
    vault: S<'_>,
    account: String,
    list: String,
    seq: u64,
    hash: B64,
) -> Result<Vec<&'static str>> {
    vault.with(|v| Ok(v.check_head(&account, &list, seq, &hash.0)?.iter().map(warning).collect()))
}

#[tauri::command]
pub async fn vault_forget_list(vault: S<'_>, account: String, list: String) -> Result<()> {
    vault.with(|v| Ok(v.forget_list(&account, &list)?))
}

#[tauri::command]
pub async fn vault_create_list(vault: S<'_>, account: String) -> Result<NewList> {
    vault.with(|v| Ok(new_list(v.create_list(&account)?)))
}

#[tauri::command]
pub async fn vault_rotate(vault: S<'_>, account: String, list: String) -> Result<SignedEntry> {
    vault.with(|v| Ok(signed(v.rotate(&account, &list)?)))
}

/// `coversSeq` makes it a snapshot; without it, an update.
#[tauri::command]
pub async fn vault_seal_content(
    vault: S<'_>,
    account: String,
    list: String,
    covers_seq: Option<u64>,
    body: B64,
) -> Result<SealedContent> {
    let kind = match covers_seq {
        None => ContentKind::Update,
        Some(covers_seq) => ContentKind::Snapshot { covers_seq },
    };
    vault.with(|v| {
        let m = v.seal_content(&account, &list, kind, &body.0)?;
        Ok(SealedContent { header: m.header.into(), nonce: m.nonce.into(), ct: m.ct.into(), sig: m.sig.into() })
    })
}

#[tauri::command]
pub async fn vault_open_contents(
    vault: S<'_>,
    account: String,
    list: String,
    messages: Vec<SealedContent>,
) -> Result<Vec<OpenedContent>> {
    let messages: Vec<RsSealedContent> = messages
        .into_iter()
        .map(|m| RsSealedContent { header: m.header.0, nonce: m.nonce.0, ct: m.ct.0, sig: m.sig.0 })
        .collect();
    vault.with(|v| {
        Ok(v.open_contents(&account, &list, &messages)?
            .into_iter()
            .map(|r| match r {
                Ok(o) => OpenedContent::Opened {
                    body: o.body.to_vec().into(),
                    epoch: o.epoch,
                    author: o.author[..].into(),
                    covers_seq: o.covers_seq,
                    head_known: o.head_known,
                },
                Err(why) => OpenedContent::Rejected { rejected: why.as_str() },
            })
            .collect())
    })
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CreatedInvite {
    entry: SignedEntry,
    /// The link secret: the one key the UI holds, because it goes into the link (E2EE §6.1).
    secret: B64,
    id: B64,
    invite_pub: B64,
    sealed_package: B64,
}

#[derive(Serialize)]
pub struct OpenedLink {
    invite: String,
    id: B64,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct InvitePreview {
    list: String,
    name: String,
    owner_name: String,
    owner: PublicIdentity,
}

#[tauri::command]
pub async fn vault_add_member(vault: S<'_>, account: String, list: String, sign_pub: B64) -> Result<SignedEntry> {
    vault.with(|v| Ok(signed(v.add_member(&account, &list, &sign_pub.0)?)))
}

#[tauri::command]
pub async fn vault_remove_member(vault: S<'_>, account: String, list: String, sign_pub: B64) -> Result<SignedEntry> {
    vault.with(|v| Ok(signed(v.remove_member(&account, &list, &sign_pub.0)?)))
}

#[tauri::command]
pub async fn vault_leave(vault: S<'_>, account: String, list: String) -> Result<SignedEntry> {
    vault.with(|v| Ok(signed(v.leave(&account, &list)?)))
}

#[tauri::command]
pub async fn vault_known_contacts(vault: S<'_>, account: String) -> Result<Vec<PublicIdentity>> {
    vault.with(|v| Ok(v.known_contacts(&account)?.iter().map(identity).collect()))
}

#[tauri::command]
pub async fn vault_safety_code(sign_pub: B64, agree_pub: B64) -> Result<Vec<String>> {
    let key = |b: &[u8]| b.try_into().map_err(|_| Error::new("InvalidArgument", "a public key is 32 bytes"));
    let id = PublicId { sign_pub: key(&sign_pub.0)?, agree_pub: key(&agree_pub.0)? };
    Ok(Vault::safety_code(&id)?.to_vec())
}

#[tauri::command]
pub async fn vault_create_invite(
    vault: S<'_>,
    account: String,
    list: String,
    expires: u64,
    name: String,
    owner_name: String,
) -> Result<CreatedInvite> {
    vault.with(|v| {
        let c = v.create_invite(&account, &list, expires, &name, &owner_name)?;
        Ok(CreatedInvite {
            entry: signed(c.entry),
            secret: c.secret[..].into(),
            id: c.id[..].into(),
            invite_pub: c.invite_pub[..].into(),
            sealed_package: c.sealed_package.into(),
        })
    })
}

#[tauri::command]
pub async fn vault_uninvite(vault: S<'_>, account: String, list: String, invite_pub: B64) -> Result<SignedEntry> {
    vault.with(|v| Ok(signed(v.uninvite(&account, &list, &invite_pub.0)?)))
}

#[tauri::command]
pub async fn vault_open_invite_link(vault: S<'_>, secret: B64) -> Result<OpenedLink> {
    vault.with(|v| {
        let (invite, id) = v.open_invite_link(&secret.0)?;
        Ok(OpenedLink { invite, id: id[..].into() })
    })
}

#[tauri::command]
pub async fn vault_read_invite_package(vault: S<'_>, invite: String, sealed: B64) -> Result<InvitePreview> {
    vault.with(|v| {
        let p = v.read_invite_package(&invite, &sealed.0)?;
        Ok(InvitePreview { owner: identity(&p.owner), list: p.list, name: p.name, owner_name: p.owner_name })
    })
}

#[tauri::command]
pub async fn vault_join(vault: S<'_>, account: String, invite: String) -> Result<SignedEntry> {
    vault.with(|v| Ok(signed(v.join(&account, &invite)?)))
}
