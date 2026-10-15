//! The vault as a Node add-on, for tests and the e2e suite (client-spec §8).
//! A thin mapping of `runer_vault::Vault` onto JavaScript values: handles
//! are strings, bytes are `Uint8Array`s, and an error's `code` is the
//! vault's error code (a log rule's name for a rejected log).

use napi::bindgen_prelude::Uint8Array;
use napi_derive::napi;
use runer_vault::log::SignedEntry as RsSignedEntry;
use runer_vault::storage::{MemoryKeychain, MemoryStore};
use runer_vault::{
    Applied as RsApplied, ContentKind, ListState as RsListState, LogEvent, PublicId, SealedContent as RsSealedContent,
    ServerMessage, StoredAccount as RsStoredAccount, VaultError, VaultWarning,
};

type Result<T> = std::result::Result<T, napi::Error<String>>;

fn err(e: VaultError) -> napi::Error<String> {
    napi::Error::new(e.code().to_owned(), e.to_string())
}

fn invalid(msg: &str) -> napi::Error<String> {
    err(VaultError::InvalidArgument(msg.to_owned()))
}

/// A JavaScript number that must be a non-negative integer.
fn uint(n: i64, name: &str) -> Result<u64> {
    u64::try_from(n).map_err(|_| invalid(&format!("{name} must not be negative")))
}

fn bytes(b: &[u8]) -> Uint8Array {
    Uint8Array::from(b.to_vec())
}

#[napi(object)]
pub struct SignIn {
    pub account: String,
    pub auth_pub: Uint8Array,
}

#[napi(object)]
pub struct SignUp {
    pub account: String,
    pub auth_pub: Uint8Array,
    pub identity: PublicIdentity,
    pub bundle: Uint8Array,
    pub inbox: NewList,
}

#[napi(object)]
pub struct Rekeyed {
    pub auth_pub: Uint8Array,
    pub bundle: Uint8Array,
}

#[napi(object)]
pub struct LoadedIdentity {
    pub identity: PublicIdentity,
    /// Warning kinds, e.g. `bundle-rollback`.
    pub warnings: Vec<String>,
}

#[napi(object)]
pub struct SignedEntry {
    pub raw: Uint8Array,
    pub sig: Uint8Array,
}

#[napi(object)]
pub struct PublicIdentity {
    pub sign_pub: Uint8Array,
    pub agree_pub: Uint8Array,
}

#[napi(object)]
pub struct Head {
    pub seq: i64,
    pub hash: Uint8Array,
}

#[napi(object)]
pub struct OpenInvite {
    pub invite_pub: Uint8Array,
    pub expires: i64,
}

#[napi(object)]
pub struct LinkJoin {
    pub member: Uint8Array,
    pub invite_pub: Uint8Array,
}

#[napi(object)]
pub struct ListState {
    pub id: String,
    pub owner: PublicIdentity,
    pub members: Vec<PublicIdentity>,
    pub epoch: i64,
    pub head: Head,
    pub open_invites: Vec<OpenInvite>,
    pub link_joins: Vec<LinkJoin>,
    pub created: i64,
    /// `member-left` or `invite-revoked`.
    pub rotation_due: Option<String>,
}

#[napi(object)]
pub struct CreatedInvite {
    pub entry: SignedEntry,
    pub secret: Uint8Array,
    pub id: Uint8Array,
    pub invite_pub: Uint8Array,
    pub sealed_package: Uint8Array,
}

#[napi(object)]
pub struct OpenedLink {
    pub invite: String,
    pub id: Uint8Array,
}

#[napi(object)]
pub struct InvitePreview {
    pub list: String,
    pub name: String,
    pub owner_name: String,
    pub owner: PublicIdentity,
}

/// `kind` is added, joined, removed, left (with `member`) or rotated (with `epoch`).
#[napi(object)]
pub struct Event {
    pub kind: String,
    pub member: Option<Uint8Array>,
    pub epoch: Option<i64>,
}

#[napi(object)]
pub struct NewList {
    pub list: String,
    pub entry: SignedEntry,
}

#[napi(object)]
pub struct Applied {
    pub state: ListState,
    pub events: Vec<Event>,
    /// Warning kinds, e.g. `log-fork`.
    pub warnings: Vec<String>,
}

fn signed(e: RsSignedEntry) -> SignedEntry {
    SignedEntry { raw: e.raw.into(), sig: e.sig.into() }
}

fn new_list(l: runer_vault::NewList) -> NewList {
    NewList { list: l.id, entry: signed(l.entry) }
}

fn identity(id: &PublicId) -> PublicIdentity {
    PublicIdentity { sign_pub: bytes(&id.sign_pub), agree_pub: bytes(&id.agree_pub) }
}

/// Every value is at most 2^53 - 1 (the parser checks), so it is exact in JavaScript.
fn num(n: u64) -> i64 {
    n as i64
}

fn list_state(s: RsListState) -> ListState {
    ListState {
        owner: identity(&s.owner),
        members: s.members.iter().map(identity).collect(),
        epoch: num(s.epoch),
        head: Head { seq: num(s.head_seq), hash: bytes(&s.head_hash) },
        open_invites: s
            .open_invites
            .iter()
            .map(|i| OpenInvite { invite_pub: bytes(&i.invite_pub), expires: num(i.expires) })
            .collect(),
        link_joins: s
            .link_joins
            .iter()
            .map(|j| LinkJoin { member: bytes(&j.member), invite_pub: bytes(&j.invite_pub) })
            .collect(),
        created: num(s.created),
        rotation_due: s.rotation_due.map(|r| r.as_str().to_owned()),
        id: s.id,
    }
}

fn event(e: &LogEvent) -> Event {
    let member = |kind: &str, k: &[u8]| Event { kind: kind.into(), member: Some(bytes(k)), epoch: None };
    match e {
        LogEvent::Added(k) => member("added", k),
        LogEvent::Joined(k) => member("joined", k),
        LogEvent::Removed(k) => member("removed", k),
        LogEvent::Left(k) => member("left", k),
        LogEvent::Rotated(epoch) => Event { kind: "rotated".into(), member: None, epoch: Some(num(*epoch)) },
    }
}

fn warning(w: &VaultWarning) -> String {
    match w {
        VaultWarning::LogFork => "log-fork".into(),
        VaultWarning::LogRollback => "log-rollback".into(),
        VaultWarning::BundleRollback => "bundle-rollback".into(),
    }
}

/// Content as it travels in `update` and `snapshot` frames (E2EE §7).
#[napi(object)]
pub struct SealedContent {
    pub header: Uint8Array,
    pub nonce: Uint8Array,
    pub ct: Uint8Array,
    pub sig: Uint8Array,
}

/// One result of `openContents`: the body and its header's fields, or `rejected` with the reason.
#[napi(object)]
pub struct OpenedContent {
    pub body: Option<Uint8Array>,
    pub epoch: Option<i64>,
    pub author: Option<Uint8Array>,
    pub covers_seq: Option<i64>,
    pub head_known: Option<bool>,
    pub rejected: Option<String>,
}

#[napi(object)]
pub struct StoredAccount {
    pub account: String,
    pub server: String,
    pub auth_pub: Uint8Array,
    pub identity: PublicIdentity,
    pub bundle_gen: i64,
    pub inbox: Option<String>,
}

fn stored_account(a: RsStoredAccount) -> StoredAccount {
    StoredAccount {
        auth_pub: bytes(&a.auth_pub),
        identity: identity(&a.identity),
        bundle_gen: num(a.bundle_gen),
        account: a.account,
        server: a.server,
        inbox: a.inbox,
    }
}

/// A device's keychain and vault store, in memory: open a second `Vault` on
/// the same device to play a restart.
#[napi]
#[derive(Default)]
pub struct MemoryDevice {
    keychain: MemoryKeychain,
    store: MemoryStore,
}

#[napi]
impl MemoryDevice {
    #[napi(constructor)]
    pub fn new() -> Self {
        Self::default()
    }
}

#[napi]
pub struct Vault {
    inner: runer_vault::Vault,
}

#[napi]
impl Vault {
    /// Opens the vault on `device`'s storage.
    #[napi(constructor)]
    pub fn new(device: &MemoryDevice) -> Result<Self> {
        let inner =
            runer_vault::Vault::open(Box::new(device.keychain.clone()), Box::new(device.store.clone())).map_err(err)?;
        Ok(Self { inner })
    }

    #[napi]
    pub fn accounts(&self) -> Vec<StoredAccount> {
        self.inner.accounts().into_iter().map(stored_account).collect()
    }

    #[napi]
    pub fn set_inbox(&mut self, account: String, list: String) -> Result<()> {
        self.inner.set_inbox(&account, &list).map_err(err)
    }

    #[napi]
    pub fn sign_in(&mut self, server: String, words: String) -> Option<SignIn> {
        let (account, auth_pub) = self.inner.sign_in(&server, &words)?;
        Some(SignIn { account, auth_pub: bytes(&auth_pub) })
    }

    #[napi]
    pub fn start_sign_up(&mut self, server: String) -> SignUp {
        let s = self.inner.start_sign_up(&server);
        SignUp {
            account: s.account,
            auth_pub: bytes(&s.auth_pub),
            identity: identity(&s.identity),
            bundle: bytes(&s.bundle),
            inbox: new_list(s.inbox),
        }
    }

    #[napi]
    pub fn solve_sign_up_pow(&self, account: String, nonce: Uint8Array, bits: u32) -> Result<Uint8Array> {
        self.inner.solve_sign_up_pow(&account, &nonce, bits).map(|b| bytes(&b)).map_err(err)
    }

    #[napi]
    pub fn confirm_sign_up(&mut self, account: String) -> Result<Vec<String>> {
        self.inner.confirm_sign_up(&account).map_err(err)
    }

    #[napi]
    pub fn sign_out(&mut self, account: String) -> Result<()> {
        self.inner.sign_out(&account).map_err(err)
    }

    #[napi]
    pub fn show_words(&self, account: String) -> Result<Vec<String>> {
        self.inner.show_words(&account).map_err(err)
    }

    #[napi]
    pub fn seal_account_settings(&self, account: String, json: String) -> Result<Uint8Array> {
        self.inner.seal_account_settings(&account, &json).map(|b| bytes(&b)).map_err(err)
    }

    #[napi]
    pub fn open_account_settings(&self, account: String, sealed: Uint8Array) -> Result<String> {
        self.inner.open_account_settings(&account, &sealed).map_err(err)
    }

    /// `kind` is login, rekey or identity.
    #[napi]
    pub fn sign_server_message(&self, account: String, kind: String, nonce: Uint8Array) -> Result<Uint8Array> {
        let kind = match kind.as_str() {
            "login" => ServerMessage::Login,
            "rekey" => ServerMessage::Rekey,
            "identity" => ServerMessage::Identity,
            _ => return Err(invalid(&format!("unknown server message {kind}"))),
        };
        self.inner.sign_server_message(&account, kind, &nonce).map(|b| bytes(&b)).map_err(err)
    }

    #[napi]
    pub fn load_identity(&mut self, account: String, bundle_gen: i64, bundle: Uint8Array) -> Result<LoadedIdentity> {
        let (id, warnings) =
            self.inner.load_identity(&account, uint(bundle_gen, "bundle_gen")?, &bundle).map_err(err)?;
        Ok(LoadedIdentity { identity: identity(&id), warnings: warnings.iter().map(warning).collect() })
    }

    #[napi]
    pub fn change_account_key(&mut self, account: String) -> Result<Rekeyed> {
        let r = self.inner.change_account_key(&account).map_err(err)?;
        Ok(Rekeyed { auth_pub: bytes(&r.auth_pub), bundle: bytes(&r.bundle) })
    }

    #[napi]
    pub fn commit_account_key(&mut self, account: String) -> Result<Vec<String>> {
        self.inner.commit_account_key(&account).map_err(err)
    }

    #[napi]
    pub fn apply_log(&mut self, account: String, list: String, entries: Vec<SignedEntry>) -> Result<Applied> {
        let entries: Vec<RsSignedEntry> =
            entries.iter().map(|e| RsSignedEntry { raw: e.raw.to_vec(), sig: e.sig.to_vec() }).collect();
        let RsApplied { state, events, warnings } = self.inner.apply_log(&account, &list, &entries).map_err(err)?;
        Ok(Applied {
            state: list_state(state),
            events: events.iter().map(event).collect(),
            warnings: warnings.iter().map(warning).collect(),
        })
    }

    #[napi]
    pub fn list_state(&self, account: String, list: String) -> Result<ListState> {
        self.inner.list_state(&account, &list).map(list_state).map_err(err)
    }

    #[napi]
    pub fn check_head(&self, account: String, list: String, seq: i64, hash: Uint8Array) -> Result<Vec<String>> {
        let w = self.inner.check_head(&account, &list, uint(seq, "seq")?, &hash).map_err(err)?;
        Ok(w.iter().map(warning).collect())
    }

    #[napi]
    pub fn forget_list(&mut self, account: String, list: String) -> Result<()> {
        self.inner.forget_list(&account, &list).map_err(err)
    }

    #[napi]
    pub fn create_list(&self, account: String) -> Result<NewList> {
        self.inner.create_list(&account).map(new_list).map_err(err)
    }

    #[napi]
    pub fn rotate(&self, account: String, list: String) -> Result<SignedEntry> {
        self.inner.rotate(&account, &list).map(signed).map_err(err)
    }

    #[napi]
    pub fn add_member(&self, account: String, list: String, sign_pub: Uint8Array) -> Result<SignedEntry> {
        self.inner.add_member(&account, &list, &sign_pub).map(signed).map_err(err)
    }

    #[napi]
    pub fn remove_member(&self, account: String, list: String, sign_pub: Uint8Array) -> Result<SignedEntry> {
        self.inner.remove_member(&account, &list, &sign_pub).map(signed).map_err(err)
    }

    #[napi]
    pub fn leave(&self, account: String, list: String) -> Result<SignedEntry> {
        self.inner.leave(&account, &list).map(signed).map_err(err)
    }

    #[napi]
    pub fn known_contacts(&self, account: String) -> Result<Vec<PublicIdentity>> {
        Ok(self.inner.known_contacts(&account).map_err(err)?.iter().map(identity).collect())
    }

    #[napi]
    pub fn safety_code(&self, sign_pub: Uint8Array, agree_pub: Uint8Array) -> Result<Vec<String>> {
        let key = |b: &[u8]| b.try_into().map_err(|_| invalid("a public key is 32 bytes"));
        let id = PublicId { sign_pub: key(&sign_pub)?, agree_pub: key(&agree_pub)? };
        Ok(runer_vault::Vault::safety_code(&id).map_err(err)?.to_vec())
    }

    #[napi]
    pub fn create_invite(
        &self,
        account: String,
        list: String,
        expires: i64,
        name: String,
        owner_name: String,
    ) -> Result<CreatedInvite> {
        let c =
            self.inner.create_invite(&account, &list, uint(expires, "expires")?, &name, &owner_name).map_err(err)?;
        Ok(CreatedInvite {
            entry: signed(c.entry),
            secret: bytes(&c.secret),
            id: bytes(&c.id),
            invite_pub: bytes(&c.invite_pub),
            sealed_package: c.sealed_package.into(),
        })
    }

    #[napi]
    pub fn uninvite(&self, account: String, list: String, invite_pub: Uint8Array) -> Result<SignedEntry> {
        self.inner.uninvite(&account, &list, &invite_pub).map(signed).map_err(err)
    }

    #[napi]
    pub fn open_invite_link(&mut self, secret: Uint8Array) -> Result<OpenedLink> {
        let (invite, id) = self.inner.open_invite_link(&secret).map_err(err)?;
        Ok(OpenedLink { invite, id: bytes(&id) })
    }

    #[napi]
    pub fn read_invite_package(&mut self, invite: String, sealed: Uint8Array) -> Result<InvitePreview> {
        let p = self.inner.read_invite_package(&invite, &sealed).map_err(err)?;
        Ok(InvitePreview { owner: identity(&p.owner), list: p.list, name: p.name, owner_name: p.owner_name })
    }

    #[napi]
    pub fn join(&mut self, account: String, invite: String) -> Result<SignedEntry> {
        self.inner.join(&account, &invite).map(signed).map_err(err)
    }

    /// `coversSeq` makes it a snapshot; without it, an update.
    #[napi]
    pub fn seal_content(
        &self,
        account: String,
        list: String,
        covers_seq: Option<i64>,
        body: Uint8Array,
    ) -> Result<SealedContent> {
        let kind = match covers_seq {
            None => ContentKind::Update,
            Some(c) => ContentKind::Snapshot { covers_seq: uint(c, "coversSeq")? },
        };
        let m = self.inner.seal_content(&account, &list, kind, &body).map_err(err)?;
        Ok(SealedContent { header: m.header.into(), nonce: m.nonce.into(), ct: m.ct.into(), sig: m.sig.into() })
    }

    #[napi]
    pub fn open_contents(
        &self,
        account: String,
        list: String,
        messages: Vec<SealedContent>,
    ) -> Result<Vec<OpenedContent>> {
        let messages: Vec<RsSealedContent> = messages
            .iter()
            .map(|m| RsSealedContent {
                header: m.header.to_vec(),
                nonce: m.nonce.to_vec(),
                ct: m.ct.to_vec(),
                sig: m.sig.to_vec(),
            })
            .collect();
        let opened = self.inner.open_contents(&account, &list, &messages).map_err(err)?;
        Ok(opened
            .into_iter()
            .map(|r| match r {
                Ok(o) => OpenedContent {
                    body: Some(bytes(&o.body)),
                    epoch: Some(num(o.epoch)),
                    author: Some(bytes(&o.author)),
                    covers_seq: o.covers_seq.map(num),
                    head_known: Some(o.head_known),
                    rejected: None,
                },
                Err(why) => OpenedContent {
                    body: None,
                    epoch: None,
                    author: None,
                    covers_seq: None,
                    head_known: None,
                    rejected: Some(why.as_str().to_owned()),
                },
            })
            .collect())
    }
}
