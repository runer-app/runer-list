use crate::log::{
    Entry, Key, LogError, LogState, Role, SignedEntry, WireEntry, WireEnvelope, encode_wire_entry, parse_entry,
    validate_entry,
};
use crate::storage::{Keychain, MemoryKeychain, MemoryStore, StorageError, Store};
use runer_crypto::SigningKeyPair;
use runer_crypto::account::{
    AccountKey, Identity, ServerMessage, account_key_to_words, derive_auth_key, derive_wrap_key, generate_account_key,
    open_account_settings, open_identity_bundle, seal_account_settings, seal_identity_bundle, words_to_account_key,
};
use runer_crypto::content::{
    ContentHeader, ContentType, SealedContent, open_content, parse_content_header, seal_content,
};
use runer_crypto::encoding::{encode_base64url, encode_hex};
use runer_crypto::list_keys::{
    EnvelopeTarget, GENESIS_LIST_ID, generate_list_key, list_id_bytes, open_envelope, open_key_chain, seal_envelope,
};
use runer_crypto::primitives::{KEY_LEN, SIG_LEN, aes_gcm_open_box, aes_gcm_seal_box, random_bytes, sha256};
use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::fmt;
use std::time::{SystemTime, UNIX_EPOCH};
use zeroize::Zeroizing;

mod sharing;

pub use sharing::{CreatedInvite, InviteHandle, InvitePreview};

/// A signed-in account on one server. A random id; it means nothing outside this vault.
pub type AccountHandle = String;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum VaultError {
    UnknownAccount,
    /// No verified log for this list yet: apply one, starting at its genesis entry.
    UnknownList,
    /// The log's genesis entry doesn't hash to the list id asked for.
    WrongList,
    InvalidArgument(String),
    /// The call doesn't fit the account's state, e.g. a commit with nothing pending.
    InvalidState(String),
    /// The Identity Bundle didn't open with this Account Key.
    BadBundle,
    /// The log broke a rule (E2EE §4.2). Nothing from the batch was kept.
    Log(LogError),
    /// The keychain or the vault's store failed. Nothing changed.
    Storage(StorageError),
}

impl VaultError {
    /// Stable code for the bindings: the variant name, or the log rule's code.
    pub fn code(&self) -> &'static str {
        match self {
            Self::UnknownAccount => "UnknownAccount",
            Self::UnknownList => "UnknownList",
            Self::WrongList => "WrongList",
            Self::InvalidArgument(_) => "InvalidArgument",
            Self::InvalidState(_) => "InvalidState",
            Self::BadBundle => "BadBundle",
            Self::Storage(_) => "Storage",
            Self::Log(e) => e.code.as_str(),
        }
    }
}

impl fmt::Display for VaultError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidArgument(d) => write!(f, "InvalidArgument: {d}"),
            Self::InvalidState(d) => write!(f, "InvalidState: {d}"),
            Self::Storage(d) => write!(f, "Storage: {d}"),
            Self::Log(e) => e.fmt(f),
            _ => f.write_str(self.code()),
        }
    }
}

impl std::error::Error for VaultError {}

impl From<LogError> for VaultError {
    fn from(e: LogError) -> Self {
        Self::Log(e)
    }
}

/// A member's two public keys.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PublicId {
    pub sign_pub: Key,
    pub agree_pub: Key,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct OpenInvite {
    pub invite_pub: Key,
    /// Unix milliseconds.
    pub expires: u64,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LinkJoin {
    pub member: Key,
    pub invite_pub: Key,
}

/// What the UI and the views may know about a list's membership, from the verified log.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ListState {
    /// Lowercase hex.
    pub id: String,
    pub owner: PublicId,
    /// Everyone in the list, the owner first, then in the order they were admitted.
    pub members: Vec<PublicId>,
    pub epoch: u64,
    pub head_seq: u64,
    pub head_hash: [u8; 32],
    pub open_invites: Vec<OpenInvite>,
    /// Members who joined with a link invite, and its key: the name the owner gave the link names them.
    pub link_joins: Vec<LinkJoin>,
    /// The genesis entry's `ts`, Unix milliseconds.
    pub created: u64,
    /// Someone outside the list may hold its current key, so it should
    /// `rotate`; `None` once an entry starts a new epoch.
    pub rotation_due: Option<RotationDue>,
}

/// Why a list's key should change, and so who rotates.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RotationDue {
    /// A member left: the next remaining member to come online rotates (E2EE §8.2).
    MemberLeft,
    /// The owner revoked a link that carried the key: the owner's app rotates (E2EE §6.1).
    InviteRevoked,
}

impl RotationDue {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::MemberLeft => "member-left",
            Self::InviteRevoked => "invite-revoked",
        }
    }
}

/// Something that happened in a log, for notices ("Anna joined").
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum LogEvent {
    Added(Key),
    Joined(Key),
    Removed(Key),
    Left(Key),
    Rotated(u64),
}

/// The log-derived warnings of client-spec §3.3 that `apply_log` can see.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum VaultWarning {
    /// The server served an entry at a seq we have verified, with a different hash (E2EE §4.4).
    LogFork,
    /// The server's head is below the one we have verified (E2EE §4.4).
    LogRollback,
    /// The server served an Identity Bundle with a lower `bundle_gen` than seen (E2EE §3.3).
    BundleRollback,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Applied {
    pub state: ListState,
    pub events: Vec<LogEvent>,
    pub warnings: Vec<VaultWarning>,
}

type ListKey = Zeroizing<[u8; KEY_LEN]>;

struct VerifiedLog {
    state: LogState,
    /// The hash of every entry, by seq: what a fork is detected against.
    hashes: Vec<[u8; 32]>,
    /// The genesis entry's `ts`.
    created: u64,
    /// The `key_chain` of the entry that started each epoch, by that epoch.
    chains: HashMap<u64, Vec<u8>>,
    /// The list keys we hold, by epoch: from envelopes to us, and the chain back from them.
    keys: HashMap<u64, ListKey>,
    /// Everyone who was a member at some point during each epoch: who may author content under it.
    writers: HashMap<u64, BTreeSet<Key>>,
    /// Everyone this log ever admitted, with the agreement key it admitted them with (E2EE §6.3).
    contacts: BTreeMap<Key, Key>,
    /// See `ListState::rotation_due`.
    rotation_due: Option<RotationDue>,
}

impl VerifiedLog {
    /// Opens our envelopes in `entries` and follows the key chain back from every key we hold.
    fn take_keys(&mut self, me: Option<&Identity>, entries: &[Entry]) {
        for e in entries {
            if !e.key_chain.is_empty() {
                self.chains.insert(e.epoch, e.key_chain.clone());
            }
            let Some(me) = me else { continue };
            let my_pub = me.sign.public_key();
            let list_id = if e.seq == 0 { &GENESIS_LIST_ID } else { &self.state.list_id };
            // An envelope that doesn't open leaves us without that key: content under it won't open either.
            if let Some(key) = e.envelopes.iter().find(|env| env.recipient == my_pub).and_then(|env| {
                open_envelope(list_id, e.epoch, &my_pub, &me.agree.private_key()[..], &env.hpke_ciphertext).ok()
            }) {
                self.keys.insert(e.epoch, key);
            }
        }
        self.follow_chain();
    }

    /// Opens the key chain back from every key we hold (E2EE §5).
    fn follow_chain(&mut self) {
        for epoch in (1..=self.state.epoch).rev() {
            if self.keys.contains_key(&(epoch - 1)) {
                continue;
            }
            let prev = match (self.keys.get(&epoch), self.chains.get(&epoch)) {
                (Some(k), Some(chain)) => open_key_chain(&k[..], chain).ok(),
                _ => None,
            };
            if let Some(prev) = prev {
                self.keys.insert(epoch - 1, prev);
            }
        }
    }
}

struct Account {
    server: String,
    key: AccountKey,
    auth: SigningKeyPair,
    /// `None` until the bundle is loaded (sign-in) or made (sign-up).
    identity: Option<Identity>,
    /// The highest `bundle_gen` seen; 0 before any.
    bundle_gen: u64,
    /// The account's Inbox (lowercase hex), once known.
    inbox: Option<String>,
    /// A change the server hasn't confirmed yet.
    pending: Option<Pending>,
    /// In the keychain and the store: from a confirmed sign-up, or a sign-in once the identity is loaded.
    saved: bool,
    lists: HashMap<String, VerifiedLog>,
}

/// Each is kept until its `commit`/`confirm`, so a failed request changes nothing.
#[allow(clippy::large_enum_variant)] // one per account at most
enum Pending {
    SignUp,
    AccountKey { key: AccountKey, auth: SigningKeyPair },
}

/// An account the vault keeps across restarts.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StoredAccount {
    pub account: AccountHandle,
    pub server: String,
    pub auth_pub: Key,
    pub identity: PublicId,
    pub bundle_gen: u64,
    /// `None` until the Inbox has been verified on this device (`set_inbox`).
    pub inbox: Option<String>,
}

/// A list's id and its signed genesis entry, not yet sent.
pub struct NewList {
    /// Lowercase hex.
    pub id: String,
    pub entry: SignedEntry,
}

/// What sign-up sends the server (E2EE §3.3), with the account's Inbox.
pub struct SignUp {
    pub account: AccountHandle,
    pub auth_pub: Key,
    pub identity: PublicId,
    pub bundle: Vec<u8>,
    pub inbox: NewList,
}

/// A new bundle and the key it was made for, waiting for the server.
pub struct Rekeyed {
    pub auth_pub: Key,
    pub bundle: Vec<u8>,
}

/// The first `bundle_gen` the server gives an account.
const FIRST_BUNDLE_GEN: u64 = 1;

impl Account {
    fn new(server: &str, key: AccountKey) -> Result<Self, VaultError> {
        let auth = derive_auth_key(&key[..]).map_err(|e| VaultError::InvalidArgument(e.to_string()))?;
        Ok(Self {
            server: server.to_owned(),
            key,
            auth,
            identity: None,
            bundle_gen: 0,
            inbox: None,
            pending: None,
            saved: false,
            lists: HashMap::new(),
        })
    }

    fn identity(&self) -> Result<&Identity, VaultError> {
        self.identity.as_ref().ok_or_else(|| VaultError::InvalidState("identity not loaded".into()))
    }
}

/// The Identity Bundle for `identity` under `key`, at `bundle_gen` (E2EE §3.3).
fn seal(key: &AccountKey, auth: &SigningKeyPair, identity: &Identity, bundle_gen: u64) -> Vec<u8> {
    let wrap = derive_wrap_key(&key[..]).expect("an Account Key is 16 bytes");
    seal_identity_bundle(&wrap[..], &auth.public_key(), bundle_gen, identity, None).expect("valid sizes")
}

fn public_id(id: &Identity) -> PublicId {
    PublicId { sign_pub: id.sign.public_key(), agree_pub: id.agree.public_key() }
}

fn new_handle() -> AccountHandle {
    format!("acct_{}", encode_hex(&random_bytes::<16>()))
}

fn leading_zero_bits(h: &[u8]) -> u32 {
    let mut n = 0;
    for b in h {
        n += b.leading_zeros();
        if *b != 0 {
            break;
        }
    }
    n
}

/// What one stored account record holds, borrowed from wherever it is
/// about to be committed, so it is written before memory changes.
struct Record<'a> {
    server: &'a str,
    key: &'a AccountKey,
    identity: &'a Identity,
    bundle_gen: u64,
    inbox: Option<&'a str>,
}

const RECORD_VERSION: u8 = 1;
const CACHE_KEY: &str = "cache-key";

fn record_name(account: &str) -> String {
    format!("acct/{account}")
}
fn account_key_name(account: &str) -> String {
    format!("account/{account}")
}
fn log_prefix(account: &str) -> String {
    format!("log/{account}/")
}
fn list_prefix(account: &str, list: &str) -> String {
    format!("log/{account}/{list}/")
}
fn entry_name(account: &str, list: &str, seq: u64) -> String {
    // 16 digits hold every seq up to 2^53, so key order is seq order.
    format!("log/{account}/{list}/{seq:016}")
}
/// List keys that came from outside the log (a link invite's package), by epoch.
fn key_prefix(account: &str) -> String {
    format!("key/{account}/")
}
fn list_key_prefix(account: &str, list: &str) -> String {
    format!("key/{account}/{list}/")
}
fn key_name(account: &str, list: &str, epoch: u64) -> String {
    format!("key/{account}/{list}/{epoch:016}")
}

/// `version ‖ sign_seed(32) ‖ agree_private(32) ‖ uint64be(bundle_gen) ‖ has_inbox ‖ inbox(32) ‖ server`.
fn encode_record(r: &Record<'_>) -> Zeroizing<Vec<u8>> {
    let mut out = Zeroizing::new(Vec::with_capacity(1 + 2 * KEY_LEN + 8 + 1 + 32 + r.server.len()));
    out.push(RECORD_VERSION);
    out.extend_from_slice(&r.identity.sign.seed()[..]);
    out.extend_from_slice(&r.identity.agree.private_key()[..]);
    out.extend_from_slice(&r.bundle_gen.to_be_bytes());
    let inbox = r.inbox.and_then(|i| list_id_bytes(i).ok());
    out.push(u8::from(inbox.is_some()));
    out.extend_from_slice(&inbox.unwrap_or([0; 32]));
    out.extend_from_slice(r.server.as_bytes());
    out
}

/// The record's identity, `bundle_gen`, Inbox and server; `None` if it isn't one.
fn decode_record(p: &[u8]) -> Option<(Identity, u64, Option<String>, String)> {
    const SERVER: usize = 1 + 2 * KEY_LEN + 8 + 1 + 32;
    if p.len() < SERVER || p[0] != RECORD_VERSION {
        return None;
    }
    let identity = Identity::from_secrets(&p[1..33], &p[33..65]).ok()?;
    let bundle_gen = u64::from_be_bytes(p[65..73].try_into().ok()?);
    let inbox = (p[73] == 1).then(|| encode_hex(&p[74..106]));
    Some((identity, bundle_gen, inbox, String::from_utf8(p[SERVER..].to_vec()).ok()?))
}

fn storage(e: StorageError) -> VaultError {
    VaultError::Storage(e)
}

/// Holds every account's keys and every verified log. What must survive a
/// restart is written through to the keychain (Account Keys, the device
/// cache key) and the vault's own store (everything else, sealed under the
/// cache key, with the record's name as AD), before memory changes.
pub struct Vault {
    accounts: HashMap<AccountHandle, Account>,
    /// Invite links opened on this device, by handle; tied to an account only by `join`.
    invites: HashMap<InviteHandle, sharing::OpenedLink>,
    keychain: Box<dyn Keychain>,
    store: Box<dyn Store>,
    cache_key: Zeroizing<Vec<u8>>,
}

impl Default for Vault {
    fn default() -> Self {
        Self::new()
    }
}

impl Vault {
    /// A vault on fresh in-memory storage, as in a test.
    pub fn new() -> Self {
        Self::open(Box::new(MemoryKeychain::default()), Box::new(MemoryStore::default()))
            .expect("empty memory storage opens")
    }

    /// Opens the vault on this device's keychain and store: every saved
    /// account, and every stored log re-verified from genesis (which opens
    /// our envelopes again). A list whose stored log no longer verifies is
    /// dropped and comes back from the server. Without the cache key the
    /// store can't be read, so it is cleared.
    pub fn open(mut keychain: Box<dyn Keychain>, mut store: Box<dyn Store>) -> Result<Self, VaultError> {
        let cache_key = match keychain.get(CACHE_KEY).map_err(storage)? {
            Some(k) if k.len() == 32 => k,
            _ => {
                for (name, _) in store.scan("").map_err(storage)? {
                    store.delete(&name).map_err(storage)?;
                }
                let k = Zeroizing::new(random_bytes::<32>().to_vec());
                keychain.set(CACHE_KEY, &k).map_err(storage)?;
                k
            }
        };
        let mut vault = Self { accounts: HashMap::new(), invites: HashMap::new(), keychain, store, cache_key };
        for (name, sealed) in vault.store.scan("acct/").map_err(storage)? {
            let handle = name["acct/".len()..].to_owned();
            match vault.load_account(&name, &sealed, &handle)? {
                Some(acct) => {
                    vault.accounts.insert(handle.clone(), acct);
                    vault.replay_logs(&handle)?;
                }
                None => vault.erase(&handle)?,
            }
        }
        Ok(vault)
    }

    fn load_account(&self, name: &str, sealed: &[u8], handle: &str) -> Result<Option<Account>, VaultError> {
        let Ok(p) = aes_gcm_open_box(&self.cache_key, sealed, name.as_bytes()) else { return Ok(None) };
        let Some((identity, bundle_gen, inbox, server)) = decode_record(&p) else { return Ok(None) };
        let Some(key) = self.keychain.get(&account_key_name(handle)).map_err(storage)? else { return Ok(None) };
        let Ok(key) = <[u8; 16]>::try_from(&key[..]) else { return Ok(None) };
        let mut acct = Account::new(&server, Zeroizing::new(key))?;
        acct.identity = Some(identity);
        acct.bundle_gen = bundle_gen;
        acct.inbox = inbox;
        acct.saved = true;
        Ok(Some(acct))
    }

    fn replay_logs(&mut self, handle: &str) -> Result<(), VaultError> {
        let mut by_list: Vec<(String, Vec<SignedEntry>)> = Vec::new();
        for (name, sealed) in self.store.scan(&log_prefix(handle)).map_err(storage)? {
            let list = name[log_prefix(handle).len()..].split('/').next().unwrap_or_default().to_owned();
            let entry = aes_gcm_open_box(&self.cache_key, &sealed, name.as_bytes())
                .ok()
                .filter(|p| p.len() > SIG_LEN)
                .map(|p| {
                    let (raw, sig) = p.split_at(p.len() - SIG_LEN);
                    SignedEntry { raw: raw.to_vec(), sig: sig.to_vec() }
                });
            match by_list.last_mut() {
                Some((l, entries)) if *l == list => entries.extend(entry),
                _ => by_list.push((list, entry.into_iter().collect())),
            }
        }
        for (list, entries) in by_list {
            let acct = self.accounts.get_mut(handle).ok_or(VaultError::UnknownAccount)?;
            match verify(acct, &list, &entries) {
                Ok(v @ Verified::New { .. }) => commit(acct, &list, v),
                _ => self.delete_list(handle, &list)?,
            }
        }
        for (name, sealed) in self.store.scan(&key_prefix(handle)).map_err(storage)? {
            let mut parts = name[key_prefix(handle).len()..].split('/');
            let (list, epoch) = (parts.next().unwrap_or_default(), parts.next().and_then(|e| e.parse().ok()));
            let key = aes_gcm_open_box(&self.cache_key, &sealed, name.as_bytes())
                .ok()
                .and_then(|k| <[u8; KEY_LEN]>::try_from(&k[..]).ok());
            let acct = self.accounts.get_mut(handle).ok_or(VaultError::UnknownAccount)?;
            match (acct.lists.get_mut(list), epoch, key) {
                (Some(log), Some(epoch), Some(key)) => {
                    log.keys.entry(epoch).or_insert_with(|| Zeroizing::new(key));
                    log.follow_chain();
                }
                _ => self.store.delete(&name).map_err(storage)?,
            }
        }
        Ok(())
    }

    fn delete_list(&mut self, handle: &str, list: &str) -> Result<(), VaultError> {
        for prefix in [list_prefix(handle, list), list_key_prefix(handle, list)] {
            for (name, _) in self.store.scan(&prefix).map_err(storage)? {
                self.store.delete(&name).map_err(storage)?;
            }
        }
        Ok(())
    }

    fn erase(&mut self, handle: &str) -> Result<(), VaultError> {
        for prefix in [log_prefix(handle), key_prefix(handle)] {
            for (name, _) in self.store.scan(&prefix).map_err(storage)? {
                self.store.delete(&name).map_err(storage)?;
            }
        }
        self.store.delete(&record_name(handle)).map_err(storage)?;
        self.keychain.delete(&account_key_name(handle)).map_err(storage)
    }

    /// The accounts kept on this device, for startup.
    pub fn accounts(&self) -> Vec<StoredAccount> {
        let mut out: Vec<_> = self
            .accounts
            .iter()
            .filter(|(_, a)| a.saved)
            .filter_map(|(h, a)| {
                Some(StoredAccount {
                    account: h.clone(),
                    server: a.server.clone(),
                    auth_pub: a.auth.public_key(),
                    identity: public_id(a.identity.as_ref()?),
                    bundle_gen: a.bundle_gen,
                    inbox: a.inbox.clone(),
                })
            })
            .collect();
        out.sort_by(|a, b| a.account.cmp(&b.account));
        out
    }

    /// Signs in with the 12 words on `server`. `None` for a typo (wrong
    /// count, unknown word, bad checksum). The same account on the same
    /// server keeps its handle. The identity comes with `load_identity`.
    pub fn sign_in(&mut self, server: &str, words: &str) -> Option<(AccountHandle, Key)> {
        let account = Account::new(server, words_to_account_key(words)?).ok()?;
        let auth_pub = account.auth.public_key();
        if let Some((h, _)) = self.accounts.iter().find(|(_, a)| a.server == server && a.auth.public_key() == auth_pub)
        {
            return Some((h.clone(), auth_pub));
        }
        let handle = new_handle();
        self.accounts.insert(handle.clone(), account);
        Some((handle, auth_pub))
    }

    /// Sign-up, step 1: a fresh Account Key and identity, and the bundle
    /// sealed at the server's first `bundle_gen`. Pending until `confirm_sign_up`.
    pub fn start_sign_up(&mut self, server: &str) -> SignUp {
        let mut account = Account::new(server, generate_account_key()).expect("a fresh key is valid");
        let identity = Identity::generate();
        let bundle = seal(&account.key, &account.auth, &identity, FIRST_BUNDLE_GEN);
        let out = SignUp {
            account: new_handle(),
            auth_pub: account.auth.public_key(),
            identity: public_id(&identity),
            bundle,
            inbox: new_list(&identity),
        };
        account.identity = Some(identity);
        account.bundle_gen = FIRST_BUNDLE_GEN;
        account.inbox = Some(out.inbox.id.clone());
        account.pending = Some(Pending::SignUp);
        self.accounts.insert(out.account.clone(), account);
        out
    }

    /// `pow_nonce` with SHA-256(auth_pub ‖ nonce ‖ pow_nonce) having `bits`
    /// leading zero bits (the server's `VerifyProofOfWork`).
    pub fn solve_sign_up_pow(&self, account: &str, nonce: &[u8], bits: u32) -> Result<Vec<u8>, VaultError> {
        let acct = self.account(account)?;
        if bits > 32 {
            return Err(VaultError::InvalidArgument(format!("{bits} proof-of-work bits")));
        }
        let prefix = [&acct.auth.public_key()[..], nonce].concat();
        let solved = (0u64..)
            .map(u64::to_be_bytes)
            .find(|c| leading_zero_bits(&sha256(&[&prefix[..], c].concat())) >= bits)
            .expect("found long before 2^64");
        Ok(solved.to_vec())
    }

    /// Sign-up, step 2, after the server accepted: saves the account and
    /// returns the words to show once.
    pub fn confirm_sign_up(&mut self, account: &str) -> Result<Vec<String>, VaultError> {
        let acct = self.accounts.get(account).ok_or(VaultError::UnknownAccount)?;
        if !matches!(acct.pending, Some(Pending::SignUp)) {
            return Err(VaultError::InvalidState("no sign-up pending".into()));
        }
        let r = Record {
            server: &acct.server,
            key: &acct.key,
            identity: acct.identity()?,
            bundle_gen: acct.bundle_gen,
            inbox: acct.inbox.as_deref(),
        };
        persist(&mut *self.store, &mut *self.keychain, &self.cache_key, account, &r)?;
        let acct = self.account_mut(account)?;
        acct.pending = None;
        acct.saved = true;
        words(&acct.key)
    }

    /// Forgets the account on this device: memory, keychain and store.
    pub fn sign_out(&mut self, account: &str) -> Result<(), VaultError> {
        self.erase(account)?;
        self.accounts.remove(account);
        Ok(())
    }

    pub fn show_words(&self, account: &str) -> Result<Vec<String>, VaultError> {
        words(&self.account(account)?.key)
    }

    /// The account's settings as the server keeps them (`settings_sealed`):
    /// only this identity opens them. `json` is the client's own JSON, which the vault
    /// doesn't read.
    pub fn seal_account_settings(&self, account: &str, json: &str) -> Result<Vec<u8>, VaultError> {
        seal_account_settings(self.account(account)?.identity()?, json, None)
            .map_err(|e| VaultError::InvalidArgument(e.to_string()))
    }

    pub fn open_account_settings(&self, account: &str, sealed: &[u8]) -> Result<String, VaultError> {
        open_account_settings(self.account(account)?.identity()?, sealed)
            .map_err(|e| VaultError::InvalidArgument(e.to_string()))
    }

    /// Signs one of the server's messages over a server nonce. `Identity`
    /// signs with the identity waiting to be registered at sign-up, and with
    /// nothing else; the rest sign with the auth key.
    pub fn sign_server_message(&self, account: &str, kind: ServerMessage, nonce: &[u8]) -> Result<Vec<u8>, VaultError> {
        let acct = self.account(account)?;
        if nonce.len() != 32 {
            return Err(VaultError::InvalidArgument("a server nonce is 32 bytes".into()));
        }
        let msg = kind.bytes(&acct.server, nonce).map_err(|e| VaultError::InvalidArgument(e.to_string()))?;
        let key = match (kind, &acct.pending) {
            (ServerMessage::Identity, Some(Pending::SignUp)) => &acct.identity()?.sign,
            (ServerMessage::Identity, _) => {
                return Err(VaultError::InvalidState("no new identity to register".into()));
            }
            _ => &acct.auth,
        };
        Ok(key.sign(&msg).to_vec())
    }

    /// Opens the bundle the server served. A lower `bundle_gen` than seen is
    /// a rollback: the bundle is ignored and the warning returned.
    pub fn load_identity(
        &mut self,
        account: &str,
        bundle_gen: u64,
        bundle: &[u8],
    ) -> Result<(PublicId, Vec<VaultWarning>), VaultError> {
        let acct = self.account_mut(account)?;
        if bundle_gen < acct.bundle_gen {
            return Ok((public_id(acct.identity()?), vec![VaultWarning::BundleRollback]));
        }
        let wrap = derive_wrap_key(&acct.key[..]).expect("an Account Key is 16 bytes");
        let identity = open_identity_bundle(&wrap[..], &acct.auth.public_key(), bundle_gen, bundle)
            .map_err(|_| VaultError::BadBundle)?;
        let acct = self.accounts.get(account).ok_or(VaultError::UnknownAccount)?;
        let r = Record {
            server: &acct.server,
            key: &acct.key,
            identity: &identity,
            bundle_gen,
            inbox: acct.inbox.as_deref(),
        };
        // A saved account's key is already in the keychain: writing it again
        // makes macOS ask for Keychain access on every start.
        if acct.saved {
            put_record(&mut *self.store, &self.cache_key, account, &r)?;
        } else {
            persist(&mut *self.store, &mut *self.keychain, &self.cache_key, account, &r)?;
        }
        let acct = self.account_mut(account)?;
        let id = public_id(&identity);
        acct.identity = Some(identity);
        acct.bundle_gen = bundle_gen;
        acct.saved = true;
        Ok((id, Vec::new()))
    }

    /// E2EE §9.2: a new Account Key, and the same identity sealed under it
    /// at the next `bundle_gen`. The old key stays until `commit_account_key`.
    pub fn change_account_key(&mut self, account: &str) -> Result<Rekeyed, VaultError> {
        let acct = self.account_mut(account)?;
        let key = generate_account_key();
        let auth = derive_auth_key(&key[..]).expect("a fresh key is valid");
        let bundle = seal(&key, &auth, acct.identity()?, acct.bundle_gen + 1);
        let out = Rekeyed { auth_pub: auth.public_key(), bundle };
        acct.pending = Some(Pending::AccountKey { key, auth });
        Ok(out)
    }

    /// After the server accepted the rekey: saves the new key and returns
    /// the new words to show once.
    pub fn commit_account_key(&mut self, account: &str) -> Result<Vec<String>, VaultError> {
        let acct = self.accounts.get(account).ok_or(VaultError::UnknownAccount)?;
        let Some(Pending::AccountKey { key, .. }) = &acct.pending else {
            return Err(VaultError::InvalidState("no account key change pending".into()));
        };
        if acct.saved {
            let r = Record {
                server: &acct.server,
                key,
                identity: acct.identity()?,
                bundle_gen: acct.bundle_gen + 1,
                inbox: acct.inbox.as_deref(),
            };
            persist(&mut *self.store, &mut *self.keychain, &self.cache_key, account, &r)?;
        }
        let acct = self.account_mut(account)?;
        let Some(Pending::AccountKey { key, auth }) = acct.pending.take() else { unreachable!("checked above") };
        acct.key = key;
        acct.auth = auth;
        acct.bundle_gen += 1;
        words(&acct.key)
    }

    /// Accepts `list` as the account's Inbox (as the server names it at
    /// sign-in) only if its verified log shows a list we own alone.
    pub fn set_inbox(&mut self, account: &str, list: &str) -> Result<(), VaultError> {
        let acct = self.accounts.get(account).ok_or(VaultError::UnknownAccount)?;
        let identity = acct.identity()?;
        let s = &acct.lists.get(list).ok_or(VaultError::UnknownList)?.state;
        let me = identity.sign.public_key();
        if s.members.len() != 1 || s.members.get(&me).map(|m| m.role) != Some(Role::Owner) {
            return Err(VaultError::InvalidState("the Inbox must be a list we own alone".into()));
        }
        if acct.saved {
            let r = Record {
                server: &acct.server,
                key: &acct.key,
                identity,
                bundle_gen: acct.bundle_gen,
                inbox: Some(list),
            };
            persist(&mut *self.store, &mut *self.keychain, &self.cache_key, account, &r)?;
        }
        self.account_mut(account)?.inbox = Some(list.to_owned());
        Ok(())
    }

    fn account(&self, account: &str) -> Result<&Account, VaultError> {
        self.accounts.get(account).ok_or(VaultError::UnknownAccount)
    }

    fn account_mut(&mut self, account: &str) -> Result<&mut Account, VaultError> {
        self.accounts.get_mut(account).ok_or(VaultError::UnknownAccount)
    }

    /// Verifies `entries` on top of the list's verified log: parsing,
    /// canonical form, signatures, rules 1–12, and that the genesis entry
    /// is the list asked for. Entries we have already verified may be sent
    /// again; one that differs from ours at the same seq is a fork, and
    /// comes back as a warning with nothing applied. The batch is
    /// all-or-nothing.
    pub fn apply_log(&mut self, account: &str, list: &str, entries: &[SignedEntry]) -> Result<Applied, VaultError> {
        let acct = self.accounts.get(account).ok_or(VaultError::UnknownAccount)?;
        let verified = verify(acct, list, entries)?;
        let (applied, events) = match &verified {
            Verified::Fork(state) => {
                return Ok(Applied { state: state.clone(), events: Vec::new(), warnings: vec![VaultWarning::LogFork] });
            }
            Verified::New { applied, events, .. } => (applied, events.clone()),
        };
        if acct.saved {
            for e in applied {
                let name = entry_name(account, list, e.seq);
                let value = Zeroizing::new([&e.raw[..], &e.sig[..]].concat());
                let sealed = aes_gcm_seal_box(&self.cache_key, &value, name.as_bytes(), None).expect("valid sizes");
                self.store.put(&name, &sealed).map_err(storage)?;
            }
        }
        let acct = self.account_mut(account)?;
        commit(acct, list, verified);
        Ok(Applied { state: public_state(&acct.lists[list]), events, warnings: Vec::new() })
    }

    pub fn list_state(&self, account: &str, list: &str) -> Result<ListState, VaultError> {
        Ok(public_state(self.log(account, list)?))
    }

    /// Compares the head the server reports for a list (`GET /groups`)
    /// with the verified log (E2EE §4.4): lower than ours is a rollback, a
    /// different hash at a seq we have is a fork. A higher seq is just news.
    pub fn check_head(
        &self,
        account: &str,
        list: &str,
        seq: u64,
        hash: &[u8],
    ) -> Result<Vec<VaultWarning>, VaultError> {
        let log = self.log(account, list)?;
        let mut warnings = Vec::new();
        if seq < log.state.head_seq {
            warnings.push(VaultWarning::LogRollback);
        }
        if usize::try_from(seq).ok().and_then(|s| log.hashes.get(s)).is_some_and(|ours| ours[..] != *hash) {
            warnings.push(VaultWarning::LogFork);
        }
        Ok(warnings)
    }

    /// Drops a list we are no longer in, or that was deleted: its log and keys.
    pub fn forget_list(&mut self, account: &str, list: &str) -> Result<(), VaultError> {
        self.account(account)?;
        self.delete_list(account, list)?;
        self.account_mut(account)?.lists.remove(list);
        Ok(())
    }

    /// A new list owned by our identity: its genesis entry, with `K_0`
    /// sealed to us. Apply it once the server has accepted it.
    pub fn create_list(&self, account: &str) -> Result<NewList, VaultError> {
        Ok(new_list(self.account(account)?.identity()?))
    }

    /// Seals content under the list's current epoch (E2EE §7). The vault
    /// writes the header itself: the list, the current epoch, our identity
    /// as author, and the verified log head.
    pub fn seal_content(
        &self,
        account: &str,
        list: &str,
        kind: ContentKind,
        body: &[u8],
    ) -> Result<SealedContent, VaultError> {
        let id = self.account(account)?.identity()?;
        let log = self.log(account, list)?;
        let s = &log.state;
        let me = id.sign.public_key();
        if !s.members.contains_key(&me) {
            return Err(VaultError::InvalidState("not a member of this list".into()));
        }
        let key =
            log.keys.get(&s.epoch).ok_or_else(|| VaultError::InvalidState("no key for the current epoch".into()))?;
        let (content_type, covers_seq) = match kind {
            ContentKind::Update => (ContentType::Update, None),
            ContentKind::Snapshot { covers_seq } => (ContentType::Snapshot, Some(covers_seq)),
        };
        let header = ContentHeader {
            content_type,
            list: s.list_id_hex(),
            epoch: s.epoch,
            covers_seq,
            author: me.to_vec(),
            log_head: s.head_hash.to_vec(),
        };
        seal_content(&key[..], &id.sign, &header, body, None).map_err(|e| VaultError::InvalidArgument(e.to_string()))
    }

    /// Opens a batch of content for one list: per message, checks the
    /// header is canonical and names this list, that its author was a
    /// member during its epoch in the verified log, then verifies the
    /// signature and decrypts. A message that fails is rejected on its own;
    /// one that opens says whether the log head it cites is in the verified
    /// log (E2EE §4.4).
    pub fn open_contents(
        &self,
        account: &str,
        list: &str,
        messages: &[SealedContent],
    ) -> Result<Vec<Result<Opened, Rejected>>, VaultError> {
        let log = self.log(account, list)?;
        Ok(messages.iter().map(|m| open_one(log, list, m)).collect())
    }

    fn log(&self, account: &str, list: &str) -> Result<&VerifiedLog, VaultError> {
        self.account(account)?.lists.get(list).ok_or(VaultError::UnknownList)
    }
}

fn open_one(log: &VerifiedLog, list: &str, m: &SealedContent) -> Result<Opened, Rejected> {
    let header = parse_content_header(&m.header).map_err(|_| Rejected::Malformed)?;
    if header.list != list {
        return Err(Rejected::WrongList);
    }
    let author: Key = header.author.as_slice().try_into().map_err(|_| Rejected::Malformed)?;
    if !log.writers.get(&header.epoch).is_some_and(|w| w.contains(&author)) {
        return Err(Rejected::NotMember);
    }
    let key = log.keys.get(&header.epoch).ok_or(Rejected::NoKey)?;
    let (header, body) = open_content(&key[..], m).map_err(|_| Rejected::BadContent)?;
    let head_known = log.hashes.iter().any(|h| h[..] == header.log_head[..]);
    Ok(Opened { body, epoch: header.epoch, author, covers_seq: header.covers_seq, head_known })
}

/// What `seal_content` makes: an update, or a snapshot covering every update up to `covers_seq`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ContentKind {
    Update,
    Snapshot { covers_seq: u64 },
}

/// Content that opened: the plaintext, and what its signed header says.
pub struct Opened {
    pub body: Zeroizing<Vec<u8>>,
    pub epoch: u64,
    pub author: Key,
    /// Snapshots only.
    pub covers_seq: Option<u64>,
    /// The header's `log_head` is an entry of the verified log. If not, the
    /// author has verified an entry we haven't: our log is behind, or the
    /// server shows the two of us different histories (E2EE §4.4).
    pub head_known: bool,
}

/// Why `open_contents` rejected one message.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Rejected {
    /// The header isn't a canonical content header.
    Malformed,
    /// The header names another list.
    WrongList,
    /// The author wasn't a member during the header's epoch, as far as the verified log goes.
    NotMember,
    /// We hold no key for the header's epoch (the log may be behind: catch it up and retry).
    NoKey,
    /// The signature doesn't verify, or the ciphertext doesn't decrypt.
    BadContent,
}

impl Rejected {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Malformed => "malformed",
            Self::WrongList => "wrong-list",
            Self::NotMember => "not-member",
            Self::NoKey => "no-key",
            Self::BadContent => "bad-content",
        }
    }
}

/// The result of checking a batch against a list's verified log.
enum Verified {
    /// An entry differs from ours at a seq we have verified; the list's state as it was.
    Fork(ListState),
    New {
        state: LogState,
        hashes: Vec<[u8; 32]>,
        events: Vec<LogEvent>,
        applied: Vec<Entry>,
        writers: HashMap<u64, BTreeSet<Key>>,
    },
}

/// Verifies `entries` on top of the list's verified log, changing nothing (see `Vault::apply_log`).
fn verify(acct: &Account, list: &str, entries: &[SignedEntry]) -> Result<Verified, VaultError> {
    let list_id = list_id_bytes(list).map_err(|e| VaultError::InvalidArgument(e.to_string()))?;
    let stored = acct.lists.get(list);
    let known: &[[u8; 32]] = stored.map_or(&[], |l| &l.hashes);

    let mut state = stored.map(|l| l.state.clone());
    let mut hashes = Vec::new();
    let mut events = Vec::new();
    let mut applied = Vec::new();
    let mut writers: HashMap<u64, BTreeSet<Key>> = HashMap::new();
    for (i, e) in entries.iter().enumerate() {
        let at = |mut err: LogError| {
            err.index = Some(i);
            err
        };
        let entry = parse_entry(&e.raw, &e.sig).map_err(at)?;
        if hashes.is_empty()
            && let Some(ours) = usize::try_from(entry.seq).ok().and_then(|s| known.get(s))
        {
            if *ours != entry.hash {
                return Ok(Verified::Fork(public_state(stored.expect("known hashes come from a stored log"))));
            }
            continue;
        }
        let next = validate_entry(state.as_ref(), &entry).map_err(at)?;
        writers.entry(next.epoch).or_default().extend(next.members.keys().copied());
        state = Some(next);
        events.extend(event_of(&entry));
        hashes.push(entry.hash);
        applied.push(entry);
    }

    let state = state.ok_or(VaultError::UnknownList)?;
    if state.list_id != list_id {
        return Err(VaultError::WrongList);
    }
    Ok(Verified::New { state, hashes, events, applied, writers })
}

/// Makes a verified batch the list's new state, and takes the keys it brings.
fn commit(acct: &mut Account, list: &str, verified: Verified) {
    let Verified::New { state, hashes, applied, writers, .. } = verified else { return };
    let log = acct.lists.entry(list.to_owned()).or_insert_with(|| VerifiedLog {
        created: applied.first().map_or(0, |e| e.ts),
        state: state.clone(),
        hashes: Vec::new(),
        chains: HashMap::new(),
        keys: HashMap::new(),
        writers: HashMap::new(),
        contacts: BTreeMap::new(),
        rotation_due: None,
    });
    log.state = state;
    log.hashes.extend(hashes);
    for (epoch, keys) in writers {
        log.writers.entry(epoch).or_default().extend(keys);
    }
    for e in &applied {
        match (e.r#type.as_str(), e.subject.zip(e.subject_agree)) {
            ("create", _) => {
                if let Some(agree) = e.author_agree {
                    log.contacts.insert(e.author, agree);
                }
            }
            ("add" | "join", Some((who, agree))) => {
                log.contacts.insert(who, agree);
            }
            // A leave lets any member rotate, so it outranks a revoked link.
            ("leave", _) => log.rotation_due = Some(RotationDue::MemberLeft),
            ("uninvite", _) => {
                log.rotation_due.get_or_insert(RotationDue::InviteRevoked);
            }
            ("remove" | "rotate", _) => log.rotation_due = None,
            _ => {}
        }
    }
    log.take_keys(acct.identity.as_ref(), &applied);
}

/// Writes an account record, then its Account Key to the keychain. A free
/// function so the record can borrow from `Vault::accounts` while it runs.
fn persist(
    store: &mut dyn Store,
    keychain: &mut dyn Keychain,
    cache_key: &[u8],
    handle: &str,
    r: &Record<'_>,
) -> Result<(), VaultError> {
    put_record(store, cache_key, handle, r)?;
    keychain.set(&account_key_name(handle), &r.key[..]).map_err(storage)
}

/// Writes an account record to the store only.
fn put_record(store: &mut dyn Store, cache_key: &[u8], handle: &str, r: &Record<'_>) -> Result<(), VaultError> {
    let name = record_name(handle);
    let sealed = aes_gcm_seal_box(cache_key, &encode_record(r), name.as_bytes(), None).expect("valid sizes");
    store.put(&name, &sealed).map_err(storage)
}

/// A genesis entry by `id`, sealing a fresh `K_0` to `id` (E2EE §5: the
/// envelope's info carries `GENESIS_LIST_ID`, since the list id is this entry's hash).
fn new_list(id: &Identity) -> NewList {
    let me = id.sign.public_key();
    let to = EnvelopeTarget { sign_pub: &me, agree_pub: &id.agree.public_key() };
    let sealed = seal_envelope(&GENESIS_LIST_ID, 0, &to, &generate_list_key()[..]).expect("valid sizes");
    let entry = sign(
        id,
        WireEntry {
            v: 1,
            r#type: "create".into(),
            author: encode_base64url(&me),
            author_agree: encode_base64url(&id.agree.public_key()),
            envelopes: vec![WireEnvelope {
                recipient: encode_base64url(&me),
                hpke_ciphertext: encode_base64url(&sealed),
            }],
            ts: now_ms().into(),
            ..Default::default()
        },
    );
    NewList { id: encode_hex(&sha256(&entry.raw)), entry }
}

fn sign(id: &Identity, w: WireEntry) -> SignedEntry {
    let raw = encode_wire_entry(&w);
    SignedEntry { sig: id.sign.sign(&raw).to_vec(), raw }
}

fn now_ms() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_millis() as u64)
}

fn words(key: &AccountKey) -> Result<Vec<String>, VaultError> {
    Ok(account_key_to_words(&key[..]).expect("an Account Key is 16 bytes").to_vec())
}

fn event_of(e: &Entry) -> Option<LogEvent> {
    match e.r#type.as_str() {
        "add" => e.subject.map(LogEvent::Added),
        "join" => Some(LogEvent::Joined(e.author)),
        "remove" => e.subject.map(LogEvent::Removed),
        "leave" => Some(LogEvent::Left(e.author)),
        "rotate" => Some(LogEvent::Rotated(e.epoch)),
        _ => None,
    }
}

fn public_state(log: &VerifiedLog) -> ListState {
    let s = &log.state;
    let mut members: Vec<_> = s.members.iter().collect();
    members.sort_by_key(|(_, m)| m.since);
    let (owner_key, owner) = members.iter().find(|(_, m)| m.role == Role::Owner).expect("a log has one owner");
    ListState {
        id: s.list_id_hex(),
        owner: PublicId { sign_pub: **owner_key, agree_pub: owner.agree },
        members: members.iter().map(|(k, m)| PublicId { sign_pub: **k, agree_pub: m.agree }).collect(),
        epoch: s.epoch,
        head_seq: s.head_seq,
        head_hash: s.head_hash,
        open_invites: s
            .invites
            .iter()
            .filter(|(_, i)| i.open)
            .map(|(k, i)| OpenInvite { invite_pub: *k, expires: i.expires })
            .collect(),
        link_joins: members
            .iter()
            .filter_map(|(k, m)| m.invite.map(|i| LinkJoin { member: **k, invite_pub: i }))
            .collect(),
        created: log.created,
        rotation_due: log.rotation_due,
    }
}
