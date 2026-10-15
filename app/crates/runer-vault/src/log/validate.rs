use super::entry::{Entry, Key, parse_entry};
use super::error::{LogError, LogErrorCode as C};
use runer_crypto::invite::{PublicIdentity, verify_join_proof};
use std::collections::{BTreeMap, BTreeSet};

const DAY_MS: u64 = 24 * 60 * 60 * 1000;
/// A link invite may expire at most this long after its own `ts` (rule 9).
pub const MAX_INVITE_LIFETIME_MS: u64 = 7 * DAY_MS;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Role {
    Owner,
    Member,
}

/// A member as the log admitted them.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Member {
    pub role: Role,
    /// X25519 key from the entry that admitted them (`author_agree` or `subject_agree`).
    pub agree: Key,
    /// Seq of that entry.
    pub since: u64,
    /// The link invite's key, for a member who joined with one.
    pub invite: Option<Key>,
}

/// One link invite as the log left it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct InviteState {
    /// Unix milliseconds.
    pub expires: u64,
    /// False once joined, uninvited, or voided by an epoch change.
    pub open: bool,
}

/// What `validate_entry` needs to check the next entry (E2EE §4.2). A list
/// with no entries yet has no state (`None`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LogState {
    /// SHA-256 of the genesis entry; every later entry's `group` is its hex (rule 1).
    pub list_id: [u8; 32],
    pub head_seq: u64,
    pub head_hash: [u8; 32],
    pub epoch: u64,
    pub last_ts: u64,
    /// By identity_sign_pub. Exactly one owner.
    pub members: BTreeMap<Key, Member>,
    /// Every link-invite key ever opened here; closed ones stay, so a key is never reused.
    pub invites: BTreeMap<Key, InviteState>,
}

impl LogState {
    pub fn list_id_hex(&self) -> String {
        runer_crypto::encoding::encode_hex(&self.list_id)
    }

    fn role(&self, who: &Key) -> Option<Role> {
        self.members.get(who).map(|m| m.role)
    }
}

fn require_envelope_for_each(e: &Entry, remaining: &BTreeMap<Key, Member>) -> Result<(), LogError> {
    let recipients: BTreeSet<&Key> = e.envelopes.iter().map(|env| &env.recipient).collect();
    if remaining.keys().all(|m| recipients.contains(m)) { Ok(()) } else { Err(LogError::new(C::MissingEnvelope)) }
}

/// Fields the parser guarantees for the entry's type (rule 8), read without a panic path.
fn required(k: Option<Key>, what: &str) -> Result<Key, LogError> {
    k.ok_or_else(|| LogError::malformed(format!("missing {what}")))
}

/// Checks `entry` against every validity rule of E2EE §4.2 and returns the
/// state with the entry applied. A port of Go's `grouplog.Validate`, check
/// for check, so the two reach the same verdict (and reason) on every file
/// in `protocol/log-fixtures`.
pub fn validate_entry(state: Option<&LogState>, entry: &Entry) -> Result<LogState, LogError> {
    // Rule 1: seq/prev extend the current head, and group is this list's id.
    match state {
        None => {
            if entry.seq != 0 || !entry.prev.is_empty() {
                return Err(LogError::with(C::HeadMismatch, "first entry must be seq 0 with no prev"));
            }
        }
        Some(s) => {
            if entry.seq != s.head_seq + 1 || entry.prev != s.head_hash {
                return Err(LogError::new(C::HeadMismatch));
            }
            if entry.group != s.list_id_hex() {
                return Err(LogError::malformed("group is not this list's id"));
            }
        }
    }

    // Rule 2: create is only valid as seq 0, and seq 0 must be create.
    let s = match (state, entry.r#type.as_str()) {
        (_, "create") if entry.seq != 0 => return Err(LogError::malformed("create must be seq 0")),
        (None, "create") => return genesis(entry),
        (None, _) => return Err(LogError::malformed("first entry of a list must be create")),
        (Some(s), _) => s,
    };

    // Rule 7: ts within 24h of the previous entry's ts, or later.
    if entry.ts < s.last_ts.saturating_sub(DAY_MS) {
        return Err(LogError::new(C::TimestampSkew));
    }

    let author = &entry.author;
    let mut members = s.members.clone();
    let mut invites = s.invites.clone();
    let mut epoch = s.epoch;
    let owner_only = || if s.role(author) == Some(Role::Owner) { Ok(()) } else { Err(LogError::new(C::NotOwner)) };
    let epoch_holds = || if entry.epoch == s.epoch { Ok(()) } else { Err(LogError::new(C::EpochMustHold)) };

    match entry.r#type.as_str() {
        "add" => {
            // Rule 3: only the owner adds and removes.
            owner_only()?;
            // A missing subject fails the envelope check below, as in Go.
            if entry.subject.is_some_and(|s| members.contains_key(&s)) {
                return Err(LogError::new(C::AlreadyMember));
            }
            // Rule 5: the epoch holds; one envelope wraps the CURRENT key for the new member.
            epoch_holds()?;
            let Some(subject) = entry.subject.filter(|s| entry.envelopes.iter().any(|e| e.recipient == *s)) else {
                return Err(LogError::new(C::MissingEnvelope));
            };
            let agree = required(entry.subject_agree, "subject_agree")?;
            members.insert(subject, Member { role: Role::Member, agree, since: entry.seq, invite: None });
        }

        "remove" => {
            owner_only()?;
            let subject = match entry.subject.and_then(|s| members.get(&s).map(|m| (s, m.role))) {
                None => return Err(LogError::new(C::NotMember)),
                Some((_, Role::Owner)) => return Err(LogError::new(C::OwnerStays)), // Rule 6
                Some((s, Role::Member)) => s,
            };
            // Rule 4: removal is atomic: epoch + 1, key_chain, an envelope per remaining member.
            if entry.epoch != s.epoch + 1 {
                return Err(LogError::new(C::EpochMustIncrease));
            }
            if entry.key_chain.is_empty() {
                return Err(LogError::new(C::MissingKeyChain));
            }
            members.remove(&subject);
            require_envelope_for_each(entry, &members)?;
            epoch = s.epoch + 1;
        }

        "leave" => {
            if entry.subject.as_ref() != Some(author) {
                return Err(LogError::new(C::LeaveNotSelf));
            }
            match members.get(author).map(|m| m.role) {
                None => return Err(LogError::new(C::NotMember)),
                Some(Role::Owner) => return Err(LogError::new(C::OwnerStays)), // Rule 6
                Some(Role::Member) => {}
            }
            epoch_holds()?;
            members.remove(author);
        }

        "rotate" => {
            // Any current member may rotate (rule 4 applies as for remove).
            if s.role(author).is_none() {
                return Err(LogError::new(C::NotMember));
            }
            if entry.epoch != s.epoch + 1 {
                return Err(LogError::new(C::EpochMustIncrease));
            }
            if entry.key_chain.is_empty() {
                return Err(LogError::new(C::MissingKeyChain));
            }
            require_envelope_for_each(entry, &members)?;
            epoch = s.epoch + 1;
        }

        "invite" => {
            // Rule 9: the owner opens a link invite with a fresh key, expiring within 7 days of its ts.
            owner_only()?;
            epoch_holds()?;
            let key = required(entry.invite, "invite")?;
            if invites.contains_key(&key) {
                return Err(LogError::new(C::InviteReused));
            }
            if entry.expires <= entry.ts || entry.expires > entry.ts + MAX_INVITE_LIFETIME_MS {
                return Err(LogError::new(C::InviteExpiry));
            }
            invites.insert(key, InviteState { expires: entry.expires, open: true });
        }

        "uninvite" => {
            // Rule 10: the owner closes an open invite.
            owner_only()?;
            epoch_holds()?;
            let key = required(entry.invite, "invite")?;
            match invites.get_mut(&key) {
                Some(inv) if inv.open => inv.open = false,
                _ => return Err(LogError::new(C::InviteNotOpen)),
            }
        }

        "join" => {
            // Rule 11: whoever holds an open invite's key admits themselves, once, before it expires.
            if members.contains_key(author) {
                return Err(LogError::new(C::AlreadyMember));
            }
            epoch_holds()?;
            let key = required(entry.invite, "invite")?;
            let inv = match invites.get_mut(&key) {
                Some(inv) if inv.open => inv,
                _ => return Err(LogError::new(C::InviteNotOpen)),
            };
            if entry.ts > inv.expires {
                return Err(LogError::new(C::InviteExpired));
            }
            let agree = required(entry.subject_agree, "subject_agree")?;
            let joiner = PublicIdentity { sign_pub: author.to_vec(), agree_pub: agree.to_vec() };
            if !verify_join_proof(&key, &s.list_id, &joiner, &entry.invite_proof) {
                return Err(LogError::new(C::JoinProof));
            }
            inv.open = false;
            members.insert(*author, Member { role: Role::Member, agree, since: entry.seq, invite: Some(key) });
        }

        other => return Err(LogError::malformed(format!("unknown entry type \"{other}\""))),
    }

    // Rule 12: a new epoch voids every open invite; its package carries the old key.
    if epoch != s.epoch {
        for inv in invites.values_mut() {
            inv.open = false;
        }
    }

    Ok(LogState {
        list_id: s.list_id,
        head_seq: entry.seq,
        head_hash: entry.hash,
        epoch,
        last_ts: entry.ts,
        members,
        invites,
    })
}

fn genesis(entry: &Entry) -> Result<LogState, LogError> {
    if entry.epoch != 0 {
        return Err(LogError::with(C::EpochMustHold, "genesis epoch must be 0"));
    }
    // The genesis may seal K_0 to its author, and to no one else (E2EE §5).
    if entry.envelopes.len() > 1 || entry.envelopes.iter().any(|e| e.recipient != entry.author) {
        return Err(LogError::malformed("create carries at most one envelope, for its author"));
    }
    let agree = required(entry.author_agree, "author_agree")?;
    Ok(LogState {
        list_id: entry.hash,
        head_seq: 0,
        head_hash: entry.hash,
        epoch: 0,
        last_ts: entry.ts,
        members: BTreeMap::from([(entry.author, Member { role: Role::Owner, agree, since: 0, invite: None })]),
        invites: BTreeMap::new(),
    })
}

/// A log entry as it travels: the canonical bytes and their signature.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SignedEntry {
    pub raw: Vec<u8>,
    pub sig: Vec<u8>,
}

/// Parses, verifies and applies `entries` in order on top of `state` (`None`
/// for a whole log). On failure the error's `index` is the offending entry.
pub fn apply_entries(
    state: Option<LogState>,
    entries: &[SignedEntry],
) -> Result<(Option<LogState>, Vec<Entry>), LogError> {
    let mut current = state;
    let mut parsed = Vec::with_capacity(entries.len());
    for (i, e) in entries.iter().enumerate() {
        let step = parse_entry(&e.raw, &e.sig).and_then(|entry| {
            current = Some(validate_entry(current.as_ref(), &entry)?);
            Ok(entry)
        });
        parsed.push(step.map_err(|mut err| {
            err.index = Some(i);
            err
        })?);
    }
    Ok((current, parsed))
}
