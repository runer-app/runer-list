//! Sharing (E2EE §6, §8): link invites and joins, direct adds of known
//! contacts, removal with a new epoch, leaving, and the rotation an unused
//! or revoked link calls for.

use runer_crypto::primitives::sha256;
use runer_vault::log::SignedEntry;
use runer_vault::storage::{MemoryKeychain, MemoryStore};
use runer_vault::{ContentKind, ListState, LogEvent, PublicId, Rejected, RotationDue, Vault, VaultError};
use std::time::{SystemTime, UNIX_EPOCH};

const SERVER: &str = "https://runer.test";
const HOUR: u64 = 3_600_000;

fn now() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis() as u64
}

/// One person on their own device, with the storage kept so it can restart.
struct Person {
    v: Vault,
    acct: String,
    id: PublicId,
    keychain: MemoryKeychain,
    store: MemoryStore,
}

impl Person {
    fn new() -> Self {
        let (keychain, store) = (MemoryKeychain::default(), MemoryStore::default());
        let mut v = Vault::open(Box::new(keychain.clone()), Box::new(store.clone())).unwrap();
        let s = v.start_sign_up(SERVER);
        v.confirm_sign_up(&s.account).unwrap();
        v.apply_log(&s.account, &s.inbox.id, &[s.inbox.entry]).unwrap();
        Self { v, acct: s.account, id: s.identity, keychain, store }
    }

    fn restart(&mut self) {
        self.v = Vault::open(Box::new(self.keychain.clone()), Box::new(self.store.clone())).unwrap();
    }

    fn inbox(&self) -> String {
        self.v.accounts()[0].inbox.clone().unwrap()
    }

    fn apply(&mut self, list: &str, entries: &[SignedEntry]) -> (ListState, Vec<LogEvent>) {
        let r = self.v.apply_log(&self.acct, list, entries).unwrap();
        (r.state, r.events)
    }

    fn seal(&self, list: &str, body: &[u8]) -> runer_vault::SealedContent {
        self.v.seal_content(&self.acct, list, ContentKind::Update, body).unwrap()
    }

    fn open(&self, list: &str, m: runer_vault::SealedContent) -> Result<Vec<u8>, Rejected> {
        self.v.open_contents(&self.acct, list, &[m]).unwrap().pop().unwrap().map(|o| o.body.to_vec())
    }
}

/// A list Alice owns, with every entry so far.
struct List {
    id: String,
    log: Vec<SignedEntry>,
}

impl List {
    fn create(owner: &mut Person) -> Self {
        let l = owner.v.create_list(&owner.acct).unwrap();
        owner.apply(&l.id, std::slice::from_ref(&l.entry));
        Self { id: l.id, log: vec![l.entry] }
    }

    /// Appends `e` as the server would, and applies it on the author's device.
    fn append(&mut self, author: &mut Person, e: SignedEntry) -> Vec<LogEvent> {
        self.log.push(e.clone());
        author.apply(&self.id, &[e]).1
    }
}

/// Alice makes a link for `list`; Bob opens it and joins. Returns the `join` entry, applied by both.
fn invite_and_join(alice: &mut Person, bob: &mut Person, list: &mut List) -> Vec<LogEvent> {
    let inv = alice.v.create_invite(&alice.acct, &list.id, now() + 48 * HOUR, "Groceries", "Alice").unwrap();
    list.append(alice, inv.entry.clone());

    let (handle, id) = bob.v.open_invite_link(&inv.secret).unwrap();
    assert_eq!(id, inv.id);
    let preview = bob.v.read_invite_package(&handle, &inv.sealed_package).unwrap();
    assert_eq!((preview.list.as_str(), preview.name.as_str()), (list.id.as_str(), "Groceries"));
    assert_eq!(preview.owner_name, "Alice");
    assert_eq!(preview.owner, alice.id);

    bob.apply(&list.id, &list.log);
    let join = bob.v.join(&bob.acct, &handle).unwrap();
    list.log.push(join.clone());
    bob.apply(&list.id, std::slice::from_ref(&join));
    alice.apply(&list.id, &[join]).1
}

#[test]
fn a_link_invite_lets_bob_join_and_read_the_history_even_after_a_restart() {
    let (mut alice, mut bob) = (Person::new(), Person::new());
    let mut list = List::create(&mut alice);
    let before = alice.seal(&list.id, b"milk");

    let events = invite_and_join(&mut alice, &mut bob, &mut list);
    assert_eq!(events, [LogEvent::Joined(bob.id.sign_pub)]);
    let state = alice.v.list_state(&alice.acct, &list.id).unwrap();
    assert_eq!(state.members, [alice.id.clone(), bob.id.clone()]);
    assert!(state.open_invites.is_empty(), "the join closes the invite");
    assert_eq!(
        state.link_joins.iter().map(|j| j.member).collect::<Vec<_>>(),
        [bob.id.sign_pub],
        "Bob came in with the link"
    );

    assert_eq!(bob.open(&list.id, before.clone()).unwrap(), b"milk");
    assert_eq!(alice.open(&list.id, bob.seal(&list.id, b"eggs")).unwrap(), b"eggs");

    // The list key came from the package, not an envelope: it must be kept on its own.
    bob.restart();
    assert_eq!(bob.open(&list.id, before).unwrap(), b"milk");
}

#[test]
fn a_join_after_a_rotation_checks_the_package_key_against_the_key_chain() {
    let (mut alice, mut bob) = (Person::new(), Person::new());
    let mut list = List::create(&mut alice);
    let old = alice.seal(&list.id, b"epoch 0");
    let r = alice.v.rotate(&alice.acct, &list.id).unwrap();
    list.append(&mut alice, r);

    invite_and_join(&mut alice, &mut bob, &mut list);
    assert_eq!(bob.open(&list.id, old).unwrap(), b"epoch 0", "the chain leads back from the package's key");
}

#[test]
fn a_closed_invite_or_a_package_that_does_not_match_the_log_is_refused() {
    let (mut alice, mut bob) = (Person::new(), Person::new());
    let mut list = List::create(&mut alice);
    let inv = alice.v.create_invite(&alice.acct, &list.id, now() + HOUR, "L", "Alice").unwrap();
    list.append(&mut alice, inv.entry.clone());
    let (handle, _) = bob.v.open_invite_link(&inv.secret).unwrap();

    assert!(matches!(bob.v.join(&bob.acct, &handle), Err(VaultError::InvalidState(_))), "package not read yet");
    bob.v.read_invite_package(&handle, &inv.sealed_package).unwrap();
    assert_eq!(bob.v.join(&bob.acct, &handle), Err(VaultError::UnknownList), "log not verified yet");

    let un = alice.v.uninvite(&alice.acct, &list.id, &inv.invite_pub).unwrap();
    list.append(&mut alice, un);
    bob.apply(&list.id, &list.log);
    assert!(matches!(bob.v.join(&bob.acct, &handle), Err(VaultError::InvalidState(_))), "the invite is closed");

    // Another list's package can't be read with this link, nor can garbage.
    let other = List::create(&mut alice);
    let inv2 = alice.v.create_invite(&alice.acct, &other.id, now() + HOUR, "M", "Alice").unwrap();
    assert!(bob.v.read_invite_package(&handle, &inv2.sealed_package).is_err());
    assert!(bob.v.open_invite_link(&[1; 5]).is_err());
}

#[test]
fn the_package_owner_must_own_the_log() {
    let (mut alice, mut bob, mut carol) = (Person::new(), Person::new(), Person::new());
    let mut list = List::create(&mut alice);
    let inv = alice.v.create_invite(&alice.acct, &list.id, now() + HOUR, "L", "Alice").unwrap();
    list.append(&mut alice, inv.entry);
    let (handle, _) = bob.v.open_invite_link(&inv.secret).unwrap();
    bob.v.read_invite_package(&handle, &inv.sealed_package).unwrap();

    // A server answering with Carol's list under Alice's list id fails the genesis check.
    let fake = List::create(&mut carol);
    assert_eq!(bob.v.apply_log(&bob.acct, &list.id, &fake.log).unwrap_err(), VaultError::WrongList);
}

#[test]
fn invite_guards() {
    let (mut alice, mut bob) = (Person::new(), Person::new());
    let mut list = List::create(&mut alice);
    let bad = |r: Result<runer_vault::CreatedInvite, VaultError>| matches!(r, Err(VaultError::InvalidArgument(_)));
    assert!(bad(alice.v.create_invite(&alice.acct, &list.id, now() - 1, "L", "Alice")), "already expired");
    assert!(bad(alice.v.create_invite(&alice.acct, &list.id, now() + 8 * 24 * HOUR, "L", "Alice")), "more than 7 days");
    let inbox = alice.inbox();
    assert!(matches!(
        alice.v.create_invite(&alice.acct, &inbox, now() + HOUR, "L", "Alice"),
        Err(VaultError::InvalidState(_))
    ));

    invite_and_join(&mut alice, &mut bob, &mut list);
    assert!(matches!(
        bob.v.create_invite(&bob.acct, &list.id, now() + HOUR, "L", "Alice"),
        Err(VaultError::InvalidState(_))
    ));
    assert!(matches!(alice.v.uninvite(&alice.acct, &list.id, &[3; 32]), Err(VaultError::InvalidState(_))));
}

#[test]
fn a_known_contact_is_added_directly_with_the_keys_from_a_verified_log() {
    let (mut alice, mut bob, carol) = (Person::new(), Person::new(), Person::new());
    let mut first = List::create(&mut alice);
    invite_and_join(&mut alice, &mut bob, &mut first);
    assert_eq!(alice.v.known_contacts(&alice.acct).unwrap(), [bob.id.clone()]);
    assert_eq!(bob.v.known_contacts(&bob.acct).unwrap(), [alice.id.clone()]);

    let mut second = List::create(&mut alice);
    let note = alice.seal(&second.id, b"plan");
    let add = alice.v.add_member(&alice.acct, &second.id, &bob.id.sign_pub).unwrap();
    assert_eq!(second.append(&mut alice, add), [LogEvent::Added(bob.id.sign_pub)]);
    bob.apply(&second.id, &second.log);
    assert_eq!(bob.open(&second.id, note).unwrap(), b"plan");

    let state_err = |r: Result<SignedEntry, VaultError>| matches!(r, Err(VaultError::InvalidState(_)));
    assert!(state_err(alice.v.add_member(&alice.acct, &second.id, &carol.id.sign_pub)), "not a known contact");
    assert!(state_err(alice.v.add_member(&alice.acct, &second.id, &bob.id.sign_pub)), "already a member");
    assert!(state_err(alice.v.add_member(&alice.acct, &alice.inbox(), &bob.id.sign_pub)), "the Inbox is never shared");
    assert!(state_err(bob.v.add_member(&bob.acct, &second.id, &alice.id.sign_pub)), "only the owner adds");
}

#[test]
fn removal_starts_an_epoch_the_removed_member_never_gets() {
    let (mut alice, mut bob, mut carol) = (Person::new(), Person::new(), Person::new());
    let mut list = List::create(&mut alice);
    invite_and_join(&mut alice, &mut bob, &mut list);
    invite_and_join(&mut alice, &mut carol, &mut list);
    let seen = alice.seal(&list.id, b"seen");

    let rm = alice.v.remove_member(&alice.acct, &list.id, &bob.id.sign_pub).unwrap();
    assert_eq!(list.append(&mut alice, rm), [LogEvent::Removed(bob.id.sign_pub)]);
    let state = alice.v.list_state(&alice.acct, &list.id).unwrap();
    assert_eq!((state.epoch, state.members.len()), (1, 2));

    for p in [&mut bob, &mut carol] {
        p.apply(&list.id, &list.log);
    }
    let after = alice.seal(&list.id, b"after");
    assert_eq!(carol.open(&list.id, after.clone()).unwrap(), b"after");
    assert_eq!(bob.open(&list.id, after), Err(Rejected::NoKey));
    assert_eq!(bob.open(&list.id, seen).unwrap(), b"seen", "Bob keeps what he could already read");

    let state_err = |r: Result<SignedEntry, VaultError>| matches!(r, Err(VaultError::InvalidState(_)));
    assert!(state_err(alice.v.remove_member(&alice.acct, &list.id, &bob.id.sign_pub)), "not a member");
    assert!(state_err(alice.v.remove_member(&alice.acct, &list.id, &alice.id.sign_pub)), "the owner stays");
    assert!(state_err(carol.v.remove_member(&carol.acct, &list.id, &alice.id.sign_pub)), "only the owner removes");
}

#[test]
fn a_leave_or_an_uninvite_makes_a_rotation_due_until_the_next_epoch_and_says_which() {
    let (mut alice, mut bob) = (Person::new(), Person::new());
    let mut list = List::create(&mut alice);
    invite_and_join(&mut alice, &mut bob, &mut list);
    let due = |p: &Person, list: &str| p.v.list_state(&p.acct, list).unwrap().rotation_due;
    assert_eq!(due(&alice, &list.id), None);
    assert!(matches!(alice.v.leave(&alice.acct, &list.id), Err(VaultError::InvalidState(_))), "the owner can't leave");

    let leave = bob.v.leave(&bob.acct, &list.id).unwrap();
    list.append(&mut bob, leave.clone());
    let (state, events) = alice.apply(&list.id, &[leave]);
    assert_eq!(events, [LogEvent::Left(bob.id.sign_pub)]);
    assert_eq!(state.rotation_due, Some(RotationDue::MemberLeft));
    let r = alice.v.rotate(&alice.acct, &list.id).unwrap();
    list.append(&mut alice, r);
    assert_eq!(due(&alice, &list.id), None);

    let inv = alice.v.create_invite(&alice.acct, &list.id, now() + HOUR, "L", "Alice").unwrap();
    list.append(&mut alice, inv.entry);
    assert_eq!(alice.v.list_state(&alice.acct, &list.id).unwrap().open_invites.len(), 1);
    let un = alice.v.uninvite(&alice.acct, &list.id, &inv.invite_pub).unwrap();
    list.append(&mut alice, un);
    let state = alice.v.list_state(&alice.acct, &list.id).unwrap();
    assert_eq!(state.rotation_due, Some(RotationDue::InviteRevoked));
    assert!(state.open_invites.is_empty());

    // Still due after a restart: it comes from the stored log.
    alice.restart();
    assert_eq!(due(&alice, &list.id), Some(RotationDue::InviteRevoked));
}

#[test]
fn a_leave_outranks_a_revoked_link_so_any_member_may_rotate() {
    let (mut alice, mut bob, mut carol) = (Person::new(), Person::new(), Person::new());
    let mut list = List::create(&mut alice);
    invite_and_join(&mut alice, &mut bob, &mut list);
    invite_and_join(&mut alice, &mut carol, &mut list);
    let inv = alice.v.create_invite(&alice.acct, &list.id, now() + HOUR, "L", "Alice").unwrap();
    list.append(&mut alice, inv.entry);
    let un = alice.v.uninvite(&alice.acct, &list.id, &inv.invite_pub).unwrap();
    list.append(&mut alice, un);
    bob.apply(&list.id, &list.log);
    let leave = bob.v.leave(&bob.acct, &list.id).unwrap();
    list.append(&mut alice, leave);
    assert_eq!(alice.v.list_state(&alice.acct, &list.id).unwrap().rotation_due, Some(RotationDue::MemberLeft));
    let (state, _) = carol.apply(&list.id, &list.log);
    assert_eq!(state.rotation_due, Some(RotationDue::MemberLeft));
}

#[test]
fn the_invite_entry_is_the_package_head() {
    let mut alice = Person::new();
    let list = List::create(&mut alice);
    let inv = alice.v.create_invite(&alice.acct, &list.id, now() + HOUR, "L", "Alice").unwrap();
    let mut bob = Person::new();
    let (handle, _) = bob.v.open_invite_link(&inv.secret).unwrap();
    bob.v.read_invite_package(&handle, &inv.sealed_package).unwrap();
    // Not applied by the owner yet, so a head that isn't in Bob's verified log is refused.
    bob.apply(&list.id, &list.log);
    assert!(matches!(bob.v.join(&bob.acct, &handle), Err(VaultError::InvalidState(_))));
    assert_eq!(inv.invite_pub.len(), 32);
    assert_ne!(sha256(&inv.entry.raw), [0; 32]);
}

#[test]
fn safety_codes_are_six_groups_of_five_digits() {
    let p = Person::new();
    let code = Vault::safety_code(&p.id).unwrap();
    assert_eq!(code.len(), 6);
    assert!(code.iter().all(|g| g.len() == 5 && g.bytes().all(|c| c.is_ascii_digit())));
    assert_eq!(code, Vault::safety_code(&p.id).unwrap());
}
