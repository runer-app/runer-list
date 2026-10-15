//! Sharing (E2EE §6, §8): direct adds of known contacts, link invites from
//! both sides, removal and rotation with a new epoch, and leaving. Every
//! entry is built from the verified log and signed here; the caller only
//! sends it and applies it once the server accepts.

use super::{Account, Key, PublicId, Vault, VaultError, VerifiedLog, key_name, now_ms, public_id, sign, storage};
use crate::log::{LogState, MAX_INVITE_LIFETIME_MS, Role, SignedEntry, WireEntry, WireEnvelope};
use runer_crypto::account::Identity;
use runer_crypto::encoding::{encode_base64url, encode_hex};
use runer_crypto::invite::{
    INVITE_ID_LEN, InviteKeys, InvitePackage, PackageKey, PublicIdentity, derive_invite_keys, derive_invite_sign_key,
    generate_invite_secret, open_invite_package, seal_invite_package, sign_join_proof,
};
use runer_crypto::list_keys::{
    EnvelopeTarget, generate_list_key, list_id_bytes, open_key_chain, seal_envelope, seal_key_chain,
};
use runer_crypto::primitives::{KEY_LEN, aes_gcm_seal_box, random_bytes, sha256};
use runer_crypto::safety::safety_code;
use std::collections::BTreeMap;
use zeroize::Zeroizing;

/// An invite link opened on this device. A random id; it means nothing outside this vault.
pub type InviteHandle = String;

/// What the owner sends: the `invite` entry to append, then the package to store under `id`.
pub struct CreatedInvite {
    pub entry: SignedEntry,
    /// The link secret `s`, for the URL fragment. The link is a key (E2EE §6.1).
    pub secret: Zeroizing<Vec<u8>>,
    pub id: [u8; INVITE_ID_LEN],
    pub invite_pub: Key,
    pub sealed_package: Vec<u8>,
}

/// What a link's package says, for the Join screen. The list key stays in the vault.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct InvitePreview {
    /// Lowercase hex.
    pub list: String,
    pub name: String,
    /// The owner's account name when the link was made; empty for a link made without one.
    pub owner_name: String,
    pub owner: PublicId,
}

pub(super) struct OpenedLink {
    secret: Zeroizing<Vec<u8>>,
    keys: InviteKeys,
    package: Option<InvitePackage>,
}

fn invalid_state(why: &str) -> VaultError {
    VaultError::InvalidState(why.into())
}

fn crypto(e: runer_crypto::CryptoError) -> VaultError {
    VaultError::InvalidArgument(e.to_string())
}

fn key_arg(b: &[u8], what: &str) -> Result<Key, VaultError> {
    b.try_into().map_err(|_| VaultError::InvalidArgument(format!("{what} must be {KEY_LEN} bytes")))
}

/// The next entry's common fields, by `id`, at the current epoch.
fn next(s: &LogState, id: &Identity, kind: &str) -> WireEntry {
    WireEntry {
        v: 1,
        group: s.list_id_hex(),
        seq: (s.head_seq + 1).into(),
        prev: encode_base64url(&s.head_hash),
        r#type: kind.into(),
        author: encode_base64url(&id.sign.public_key()),
        epoch: s.epoch.into(),
        ts: now_ms().into(),
        ..Default::default()
    }
}

fn envelope(s: &LogState, epoch: u64, to: (&Key, &Key), key: &[u8]) -> Result<WireEnvelope, VaultError> {
    let target = EnvelopeTarget { sign_pub: to.0, agree_pub: to.1 };
    Ok(WireEnvelope {
        recipient: encode_base64url(to.0),
        hpke_ciphertext: encode_base64url(&seal_envelope(&s.list_id, epoch, &target, key).map_err(crypto)?),
    })
}

fn current_key(log: &VerifiedLog) -> Result<&Zeroizing<[u8; KEY_LEN]>, VaultError> {
    log.keys.get(&log.state.epoch).ok_or_else(|| invalid_state("no key for the current epoch"))
}

impl Account {
    fn list(&self, list: &str) -> Result<&VerifiedLog, VaultError> {
        self.lists.get(list).ok_or(VaultError::UnknownList)
    }

    /// Our identity and the list, if we are one of its members.
    fn member_of(&self, list: &str) -> Result<(&Identity, &VerifiedLog), VaultError> {
        let id = self.identity()?;
        let log = self.list(list)?;
        if !log.state.members.contains_key(&id.sign.public_key()) {
            return Err(invalid_state("not a member of this list"));
        }
        Ok((id, log))
    }

    /// Our identity and the list, if we own it and it isn't our Inbox (which is never shared).
    fn owner_of(&self, list: &str) -> Result<(&Identity, &VerifiedLog), VaultError> {
        let (id, log) = self.member_of(list)?;
        if log.state.members[&id.sign.public_key()].role != Role::Owner {
            return Err(invalid_state("only the owner can do this"));
        }
        if self.inbox.as_deref() == Some(list) {
            return Err(invalid_state("the Inbox is never shared"));
        }
        Ok((id, log))
    }
}

impl Vault {
    /// Owner: adds someone from a verified log of any of our lists, with an
    /// envelope of the current key to the agreement key that log admitted
    /// them with (E2EE §6.3). Never from a key the server names.
    pub fn add_member(&self, account: &str, list: &str, sign_pub: &[u8]) -> Result<SignedEntry, VaultError> {
        let acct = self.account(account)?;
        let (id, log) = acct.owner_of(list)?;
        let who = key_arg(sign_pub, "a sign key")?;
        if log.state.members.contains_key(&who) {
            return Err(invalid_state("already a member"));
        }
        let agree = acct
            .lists
            .values()
            .find_map(|l| l.contacts.get(&who))
            .ok_or_else(|| invalid_state("not in a verified log of any of our lists"))?;
        let s = &log.state;
        let env = envelope(s, s.epoch, (&who, agree), &current_key(log)?[..])?;
        Ok(sign(
            id,
            WireEntry {
                subject: encode_base64url(&who),
                subject_agree: encode_base64url(agree),
                envelopes: vec![env],
                ..next(s, id, "add")
            },
        ))
    }

    /// Owner: removes a member, with a new epoch sealed to everyone else (E2EE §8.1).
    pub fn remove_member(&self, account: &str, list: &str, sign_pub: &[u8]) -> Result<SignedEntry, VaultError> {
        let acct = self.account(account)?;
        let (id, log) = acct.owner_of(list)?;
        let who = key_arg(sign_pub, "a sign key")?;
        match log.state.members.get(&who).map(|m| m.role) {
            None => Err(invalid_state("not a member")),
            Some(Role::Owner) => Err(invalid_state("the owner can't be removed; delete the list instead")),
            Some(Role::Member) => new_epoch(id, log, "remove", Some(who)),
        }
    }

    /// Any member: the next epoch, its fresh key sealed to every member of
    /// the verified log, and `key_chain` back to the current key (E2EE §5).
    /// Apply it once the server has accepted it; until then nothing changes.
    pub fn rotate(&self, account: &str, list: &str) -> Result<SignedEntry, VaultError> {
        let (id, log) = self.account(account)?.member_of(list)?;
        new_epoch(id, log, "rotate", None)
    }

    /// A member other than the owner leaves (E2EE §8.2). The next member to
    /// come online rotates (`RotationDue::MemberLeft`).
    pub fn leave(&self, account: &str, list: &str) -> Result<SignedEntry, VaultError> {
        let (id, log) = self.account(account)?.member_of(list)?;
        let me = id.sign.public_key();
        if log.state.members[&me].role == Role::Owner {
            return Err(invalid_state("the owner can't leave; delete the list instead"));
        }
        Ok(sign(id, WireEntry { subject: encode_base64url(&me), ..next(&log.state, id, "leave") }))
    }

    /// Everyone we share, or shared, a verified log with: the "Share with…" candidates.
    pub fn known_contacts(&self, account: &str) -> Result<Vec<PublicId>, VaultError> {
        let acct = self.account(account)?;
        let me = acct.identity()?.sign.public_key();
        // By sign key: sorted, each once.
        let all: BTreeMap<&Key, &Key> = acct.lists.values().flat_map(|l| &l.contacts).collect();
        Ok(all
            .into_iter()
            .filter(|(sign_pub, _)| **sign_pub != me)
            .map(|(sign_pub, agree_pub)| PublicId { sign_pub: *sign_pub, agree_pub: *agree_pub })
            .collect())
    }

    /// One identity's safety code: six groups of five digits (E2EE §6.4).
    pub fn safety_code(id: &PublicId) -> Result<[String; 6], VaultError> {
        safety_code(&id.sign_pub, &id.agree_pub).map_err(crypto)
    }

    /// Owner: a link invite (E2EE §6.1). The `invite` entry, and the
    /// package for it, sealed under the new link secret: the current epoch
    /// and its key, with the `invite` entry as the log head. `name` is the
    /// list's and `owner_name` the owner's own, both for the Join screen.
    /// Append the entry first; the server stores the package only for an
    /// open invite.
    pub fn create_invite(
        &self,
        account: &str,
        list: &str,
        expires: u64,
        name: &str,
        owner_name: &str,
    ) -> Result<CreatedInvite, VaultError> {
        let acct = self.account(account)?;
        let (id, log) = acct.owner_of(list)?;
        let s = &log.state;
        let now = now_ms();
        if expires <= now || expires > now + MAX_INVITE_LIFETIME_MS {
            return Err(VaultError::InvalidArgument("an invite expires within 7 days".into()));
        }
        let key = current_key(log)?;
        let secret = generate_invite_secret();
        let keys = derive_invite_keys(&secret[..]).map_err(crypto)?;
        let invite_pub = derive_invite_sign_key(&secret[..]).map_err(crypto)?.public_key();
        let entry = sign(
            id,
            WireEntry {
                invite: encode_base64url(&invite_pub),
                expires: expires.into(),
                ts: now.into(),
                ..next(s, id, "invite")
            },
        );
        let package = InvitePackage {
            list: s.list_id_hex(),
            log_head: sha256(&entry.raw).to_vec(),
            owner: PublicIdentity {
                sign_pub: id.sign.public_key().to_vec(),
                agree_pub: id.agree.public_key().to_vec(),
            },
            name: name.to_owned(),
            owner_name: owner_name.to_owned(),
            key: Some(PackageKey { epoch: s.epoch, list_key: Zeroizing::new(key.to_vec()) }),
        };
        let sealed_package = seal_invite_package(&keys, &package, None).map_err(crypto)?;
        Ok(CreatedInvite { entry, secret: Zeroizing::new(secret.to_vec()), id: keys.id, invite_pub, sealed_package })
    }

    /// Owner: closes an open link invite. The unused link carried the list
    /// key, so a rotation is due once it is applied (E2EE §6.1).
    pub fn uninvite(&self, account: &str, list: &str, invite_pub: &[u8]) -> Result<SignedEntry, VaultError> {
        let (id, log) = self.account(account)?.owner_of(list)?;
        let key = key_arg(invite_pub, "an invite key")?;
        if !log.state.invites.get(&key).is_some_and(|i| i.open) {
            return Err(invalid_state("no open invite with this key"));
        }
        Ok(sign(id, WireEntry { invite: encode_base64url(&key), ..next(&log.state, id, "uninvite") }))
    }

    /// Joiner, step 1: keeps the link secret and gives the invite id to fetch the package with.
    pub fn open_invite_link(&mut self, secret: &[u8]) -> Result<(InviteHandle, [u8; INVITE_ID_LEN]), VaultError> {
        let keys = derive_invite_keys(secret).map_err(crypto)?;
        let id = keys.id;
        let handle = format!("inv_{}", encode_hex(&random_bytes::<16>()));
        self.invites
            .insert(handle.clone(), OpenedLink { secret: Zeroizing::new(secret.to_vec()), keys, package: None });
        Ok((handle, id))
    }

    /// Joiner, step 2: opens the package the server served for the link. It
    /// must be a link invite's (it carries the list key).
    pub fn read_invite_package(&mut self, invite: &str, sealed: &[u8]) -> Result<InvitePreview, VaultError> {
        let link = self.invites.get_mut(invite).ok_or_else(|| VaultError::InvalidArgument("unknown invite".into()))?;
        let package = open_invite_package(&link.keys, sealed)
            .map_err(|_| VaultError::InvalidArgument("the package doesn't open with this link".into()))?;
        if package.key.is_none() {
            return Err(VaultError::InvalidArgument("not a link invite".into()));
        }
        list_id_bytes(&package.list).map_err(crypto)?;
        let owner = PublicId {
            sign_pub: key_arg(&package.owner.sign_pub, "the owner's sign key")?,
            agree_pub: key_arg(&package.owner.agree_pub, "the owner's agree key")?,
        };
        let preview = InvitePreview {
            list: package.list.clone(),
            name: package.name.clone(),
            owner_name: package.owner_name.clone(),
            owner,
        };
        link.package = Some(package);
        Ok(preview)
    }

    /// Joiner, step 3, after `apply_log` verified the list's log from
    /// genesis: checks the package against it (the owner, the head, the
    /// invite still open, the key against the key chain), keeps the list
    /// key, and returns our `join` entry (E2EE §6.1).
    pub fn join(&mut self, account: &str, invite: &str) -> Result<SignedEntry, VaultError> {
        let link = self.invites.get(invite).ok_or_else(|| VaultError::InvalidArgument("unknown invite".into()))?;
        let package = link.package.as_ref().ok_or_else(|| invalid_state("read the invite's package first"))?;
        let pkg_key = package.key.as_ref().expect("read_invite_package keeps link invites only");
        let acct = self.accounts.get(account).ok_or(VaultError::UnknownAccount)?;
        let id = acct.identity()?;
        let log = acct.list(&package.list)?;
        let s = &log.state;
        let owner = s.members.iter().find(|(_, m)| m.role == Role::Owner).expect("a log has one owner");
        if owner.0[..] != package.owner.sign_pub[..] || owner.1.agree[..] != package.owner.agree_pub[..] {
            return Err(invalid_state("the package's owner doesn't own this list"));
        }
        if !log.hashes.iter().any(|h| h[..] == package.log_head[..]) {
            return Err(invalid_state("the package's log head isn't in the verified log"));
        }
        let me = public_id(id);
        if s.members.contains_key(&me.sign_pub) {
            return Err(invalid_state("already a member"));
        }
        let invite_key = derive_invite_sign_key(&link.secret).map_err(crypto)?;
        let invite_pub = invite_key.public_key();
        match s.invites.get(&invite_pub) {
            Some(i) if i.open && now_ms() <= i.expires => {}
            Some(i) if i.open => return Err(invalid_state("the invite expired")),
            _ => return Err(invalid_state("the invite is not open")),
        }
        let list_key: [u8; KEY_LEN] = key_arg(&pkg_key.list_key, "the list key")?;
        let chain_ok = match pkg_key.epoch {
            0 => true, // nothing in the log commits to K_0; the package is authenticated by the link
            e => log.chains.get(&e).is_some_and(|c| open_key_chain(&list_key, c).is_ok()),
        };
        if pkg_key.epoch != s.epoch || !chain_ok {
            return Err(invalid_state("the package's key isn't the list's current key"));
        }
        let joiner = PublicIdentity { sign_pub: me.sign_pub.to_vec(), agree_pub: me.agree_pub.to_vec() };
        let proof = sign_join_proof(&invite_key, &s.list_id, &joiner).map_err(crypto)?;
        let entry = sign(
            id,
            WireEntry {
                subject: encode_base64url(&me.sign_pub),
                subject_agree: encode_base64url(&me.agree_pub),
                invite: encode_base64url(&invite_pub),
                invite_proof: encode_base64url(&proof),
                ..next(s, id, "join")
            },
        );

        // The key arrived outside the log, so it is kept on its own (it comes back at startup).
        let (list, epoch) = (package.list.clone(), pkg_key.epoch);
        if acct.saved {
            let name = key_name(account, &list, epoch);
            let sealed = aes_gcm_seal_box(&self.cache_key, &list_key, name.as_bytes(), None).expect("valid sizes");
            self.store.put(&name, &sealed).map_err(storage)?;
        }
        let log = self.accounts.get_mut(account).and_then(|a| a.lists.get_mut(&list)).expect("checked above");
        log.keys.insert(epoch, Zeroizing::new(list_key));
        log.follow_chain();
        Ok(entry)
    }
}

/// A `rotate`, or a `remove` of `removed`: a fresh key for the next epoch,
/// sealed to every remaining member, with `key_chain` back to the current key.
fn new_epoch(id: &Identity, log: &VerifiedLog, kind: &str, removed: Option<Key>) -> Result<SignedEntry, VaultError> {
    let s = &log.state;
    let current = current_key(log)?;
    let next_key = generate_list_key();
    let epoch = s.epoch + 1;
    let envelopes = s
        .members
        .iter()
        .filter(|(k, _)| Some(**k) != removed)
        .map(|(k, m)| envelope(s, epoch, (k, &m.agree), &next_key[..]))
        .collect::<Result<_, _>>()?;
    let key_chain = seal_key_chain(&next_key[..], &current[..]).expect("two 32-byte keys");
    Ok(sign(
        id,
        WireEntry {
            subject: removed.map(|k| encode_base64url(&k)).unwrap_or_default(),
            epoch: epoch.into(),
            envelopes,
            key_chain: encode_base64url(&key_chain),
            ..next(s, id, kind)
        },
    ))
}
