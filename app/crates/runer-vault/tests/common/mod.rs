//! Builds logs entry by entry, signed with test keys, so a test can break
//! exactly one rule.
#![allow(dead_code)]

use runer_crypto::SigningKeyPair;
use runer_crypto::encoding::{encode_base64url, encode_hex};
use runer_crypto::invite::{PublicIdentity, derive_invite_sign_key, generate_invite_secret, sign_join_proof};
use runer_crypto::list_keys::list_id_bytes;
use runer_crypto::primitives::{random_bytes, sha256};
use runer_vault::log::{SignedEntry, WireEntry, WireEnvelope, encode_wire_entry};
use std::path::PathBuf;

pub fn fixtures_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../protocol/log-fixtures")
}

/// Standard base64 with padding, as Go writes `[]byte` in JSON (the fixture files).
pub fn std_base64(s: &str) -> Vec<u8> {
    const A: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let (mut acc, mut bits, mut out) = (0u32, 0, Vec::new());
    for c in s.bytes().filter(|&c| c != b'=') {
        acc = (acc << 6 | A.iter().position(|&x| x == c).expect("base64") as u32) & 0xfff;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
        }
    }
    out
}

/// A test identity: an Ed25519 key and an X25519 public key (random bytes;
/// the log never uses it as a key).
#[derive(Clone, Copy)]
pub struct Id {
    seed: [u8; 32],
    pub public: [u8; 32],
    pub agree: [u8; 32],
}

impl Id {
    pub fn new() -> Self {
        Self::from_key(&SigningKeyPair::generate())
    }

    /// A link invite's signing key.
    pub fn invite_key() -> Self {
        Self::from_key(&derive_invite_sign_key(&generate_invite_secret()[..]).unwrap())
    }

    fn from_key(k: &SigningKeyPair) -> Self {
        Self { seed: *k.seed(), public: k.public_key(), agree: random_bytes() }
    }

    pub fn key(&self) -> SigningKeyPair {
        SigningKeyPair::from_seed(&self.seed).unwrap()
    }

    pub fn b64(&self) -> String {
        b(&self.public)
    }
}

pub fn b(bytes: &[u8]) -> String {
    encode_base64url(bytes)
}

/// Canonically encodes and signs an entry with arbitrary (possibly invalid) field values.
pub fn sign_entry(author: &Id, w: WireEntry) -> SignedEntry {
    let raw = encode_wire_entry(&w);
    SignedEntry { sig: author.key().sign(&raw).to_vec(), raw }
}

pub fn envelope(to: &Id) -> WireEnvelope {
    WireEnvelope { recipient: to.b64(), hpke_ciphertext: b(&[1, 2, 3]) }
}

/// No field overrides.
pub fn none(_: &mut WireEntry) {}

/// A log under construction. Each candidate is a correct next entry with
/// optional overrides; `append` commits it as the new head.
pub struct LogBuilder {
    pub owner: Id,
    pub entries: Vec<SignedEntry>,
    pub epoch: u64,
    pub ts: i128,
    pub group: String,
    seq: i128,
    head: [u8; 32],
}

impl LogBuilder {
    pub fn create(owner: Id) -> Self {
        let ts = 1_767_268_800_000;
        let mut log = Self { owner, entries: Vec::new(), epoch: 0, ts, group: String::new(), seq: -1, head: [0; 32] };
        let genesis = sign_entry(
            &owner,
            WireEntry {
                v: 1,
                r#type: "create".into(),
                author: owner.b64(),
                author_agree: b(&owner.agree),
                ts,
                ..Default::default()
            },
        );
        log.append(genesis);
        log.group = encode_hex(&log.head);
        log
    }

    pub fn append(&mut self, e: SignedEntry) {
        self.head = sha256(&e.raw);
        self.entries.push(e);
        self.seq += 1;
    }

    /// The next entry's base fields; each candidate moves `ts` on by a second.
    fn base(&mut self, author: &Id, kind: &str) -> WireEntry {
        self.ts += 1000;
        WireEntry {
            v: 1,
            group: self.group.clone(),
            seq: self.seq + 1,
            prev: b(&self.head),
            r#type: kind.into(),
            author: author.b64(),
            epoch: self.epoch.into(),
            ts: self.ts,
            ..Default::default()
        }
    }

    fn sign(author: &Id, mut w: WireEntry, over: impl FnOnce(&mut WireEntry)) -> SignedEntry {
        over(&mut w);
        sign_entry(author, w)
    }

    pub fn add_by(&mut self, author: &Id, who: &Id, over: impl FnOnce(&mut WireEntry)) -> SignedEntry {
        let mut w = self.base(author, "add");
        w.subject = who.b64();
        w.subject_agree = b(&who.agree);
        w.envelopes = vec![envelope(who)];
        Self::sign(author, w, over)
    }

    pub fn add(&mut self, who: &Id, over: impl FnOnce(&mut WireEntry)) -> SignedEntry {
        let owner = self.owner;
        self.add_by(&owner, who, over)
    }

    pub fn remove_by(
        &mut self,
        author: &Id,
        who: &Id,
        remaining: &[Id],
        over: impl FnOnce(&mut WireEntry),
    ) -> SignedEntry {
        let mut w = self.base(author, "remove");
        w.epoch += 1;
        w.key_chain = b(&[9]);
        w.subject = who.b64();
        w.envelopes = remaining.iter().map(envelope).collect();
        Self::sign(author, w, over)
    }

    pub fn remove(&mut self, who: &Id, remaining: &[Id], over: impl FnOnce(&mut WireEntry)) -> SignedEntry {
        let owner = self.owner;
        self.remove_by(&owner, who, remaining, over)
    }

    pub fn leave(&mut self, who: &Id, over: impl FnOnce(&mut WireEntry)) -> SignedEntry {
        let mut w = self.base(who, "leave");
        w.subject = who.b64();
        Self::sign(who, w, over)
    }

    pub fn rotate(&mut self, author: &Id, everyone: &[Id], over: impl FnOnce(&mut WireEntry)) -> SignedEntry {
        let mut w = self.base(author, "rotate");
        w.epoch += 1;
        w.key_chain = b(&[9]);
        w.envelopes = everyone.iter().map(envelope).collect();
        Self::sign(author, w, over)
    }

    /// An `invite` for `key`, expiring `lifetime` ms after its ts.
    pub fn invite_by(
        &mut self,
        author: &Id,
        key: &Id,
        lifetime: i128,
        over: impl FnOnce(&mut WireEntry),
    ) -> SignedEntry {
        let mut w = self.base(author, "invite");
        w.invite = key.b64();
        w.expires = w.ts + lifetime;
        Self::sign(author, w, over)
    }

    pub fn invite(&mut self, key: &Id, lifetime: i128, over: impl FnOnce(&mut WireEntry)) -> SignedEntry {
        let owner = self.owner;
        self.invite_by(&owner, key, lifetime, over)
    }

    pub fn uninvite_by(&mut self, author: &Id, key: &Id, over: impl FnOnce(&mut WireEntry)) -> SignedEntry {
        let mut w = self.base(author, "uninvite");
        w.invite = key.b64();
        Self::sign(author, w, over)
    }

    pub fn uninvite(&mut self, key: &Id, over: impl FnOnce(&mut WireEntry)) -> SignedEntry {
        let owner = self.owner;
        self.uninvite_by(&owner, key, over)
    }

    /// `who` joins with a proof signed by `key` for this list.
    pub fn join(&mut self, who: &Id, key: &Id, over: impl FnOnce(&mut WireEntry)) -> SignedEntry {
        let joiner = PublicIdentity { sign_pub: who.public.to_vec(), agree_pub: who.agree.to_vec() };
        let proof = sign_join_proof(&key.key(), &list_id_bytes(&self.group).unwrap(), &joiner).unwrap();
        let mut w = self.base(who, "join");
        w.subject = who.b64();
        w.subject_agree = b(&who.agree);
        w.invite = key.b64();
        w.invite_proof = b(&proof);
        Self::sign(who, w, over)
    }
}

pub const HOUR: i128 = 3_600_000;
pub const DAY: i128 = 24 * HOUR;
