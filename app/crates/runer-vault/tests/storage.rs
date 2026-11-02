//! What survives a restart: accounts in the keychain and
//! the store, logs re-verified from the store, keys opened again from them.

mod common;

use common::{Id, LogBuilder};
use runer_vault::storage::{Keychain, MemoryKeychain, MemoryStore, StorageError, Store};
use runer_vault::{Vault, VaultError};
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use zeroize::Zeroizing;

const SERVER: &str = "https://runer.test";

#[derive(Clone, Default)]
struct Device {
    keychain: MemoryKeychain,
    store: MemoryStore,
}

impl Device {
    fn open(&self) -> Vault {
        Vault::open(Box::new(self.keychain.clone()), Box::new(self.store.clone())).unwrap()
    }
}

/// A confirmed sign-up with its Inbox applied, and one more list at epoch 1.
fn signed_up(d: &Device) -> (Vault, String, String, String) {
    let mut v = d.open();
    let s = v.start_sign_up(SERVER);
    v.confirm_sign_up(&s.account).unwrap();
    v.apply_log(&s.account, &s.inbox.id, &[s.inbox.entry]).unwrap();
    let list = v.create_list(&s.account).unwrap();
    v.apply_log(&s.account, &list.id, &[list.entry]).unwrap();
    let r = v.rotate(&s.account, &list.id).unwrap();
    v.apply_log(&s.account, &list.id, &[r]).unwrap();
    (v, s.account, s.inbox.id, list.id)
}

#[test]
fn accounts_logs_and_keys_survive_a_restart() {
    let d = Device::default();
    let (v, acct, inbox, list) = signed_up(&d);

    let mut after = d.open();
    assert_eq!(after.accounts(), v.accounts());
    let stored = &after.accounts()[0];
    assert_eq!((stored.account.as_str(), stored.server.as_str()), (acct.as_str(), SERVER));
    assert_eq!((stored.bundle_gen, stored.inbox.as_deref()), (1, Some(inbox.as_str())));
    assert_eq!(after.show_words(&acct).unwrap(), v.show_words(&acct).unwrap());
    assert_eq!(after.list_state(&acct, &list).unwrap(), v.list_state(&acct, &list).unwrap());
    // Rotating needs the current key: it was opened again from the stored log.
    let r = after.rotate(&acct, &list).unwrap();
    after.apply_log(&acct, &list, &[r]).unwrap();
    assert_eq!(d.open().list_state(&acct, &list).unwrap().epoch, 2);
}

/// Counts writes: on macOS each one can ask the user for Keychain access.
struct CountingKeychain(MemoryKeychain, Arc<AtomicUsize>);

impl Keychain for CountingKeychain {
    fn get(&self, name: &str) -> Result<Option<Zeroizing<Vec<u8>>>, StorageError> {
        self.0.get(name)
    }
    fn set(&mut self, name: &str, secret: &[u8]) -> Result<(), StorageError> {
        self.1.fetch_add(1, Ordering::SeqCst);
        self.0.set(name, secret)
    }
    fn delete(&mut self, name: &str) -> Result<(), StorageError> {
        self.0.delete(name)
    }
}

#[test]
fn a_restart_reloading_the_identity_does_not_write_the_keychain() {
    let d = Device::default();
    let mut v = d.open();
    let s = v.start_sign_up(SERVER);
    v.confirm_sign_up(&s.account).unwrap();

    let writes = Arc::new(AtomicUsize::new(0));
    let mut after =
        Vault::open(Box::new(CountingKeychain(d.keychain.clone(), writes.clone())), Box::new(d.store.clone())).unwrap();
    after.load_identity(&s.account, 1, &s.bundle).unwrap();
    assert_eq!(writes.load(Ordering::SeqCst), 0);
    assert_eq!(d.open().accounts()[0].identity, s.identity, "the record is still saved");
}

#[test]
fn a_pending_sign_up_or_sign_in_without_identity_is_not_kept() {
    let d = Device::default();
    let mut v = d.open();
    let s = v.start_sign_up(SERVER);
    let mut other = Vault::new();
    let words = {
        let o = other.start_sign_up(SERVER);
        other.confirm_sign_up(&o.account).unwrap()
    };
    v.sign_in(SERVER, &words.join(" ")).unwrap();
    assert!(v.accounts().is_empty());
    assert!(d.open().accounts().is_empty());
    v.confirm_sign_up(&s.account).unwrap();
    assert_eq!(d.open().accounts().len(), 1);
}

#[test]
fn sign_in_saves_the_account_once_the_identity_loads_and_checks_the_inbox() {
    let mut first = Vault::new();
    let s = first.start_sign_up(SERVER);
    let words = first.confirm_sign_up(&s.account).unwrap();

    let d = Device::default();
    let mut v = d.open();
    let (acct, _) = v.sign_in(SERVER, &words.join(" ")).unwrap();
    v.load_identity(&acct, 1, &s.bundle).unwrap();
    assert_eq!(d.open().accounts()[0].inbox, None);

    // Someone else's list is not an Inbox; our own private one is.
    let foreign = LogBuilder::create(Id::new());
    v.apply_log(&acct, &foreign.group, &foreign.entries).unwrap();
    assert!(matches!(v.set_inbox(&acct, &foreign.group), Err(VaultError::InvalidState(_))));
    assert_eq!(v.set_inbox(&acct, &s.inbox.id), Err(VaultError::UnknownList));
    v.apply_log(&acct, &s.inbox.id, &[s.inbox.entry]).unwrap();
    v.set_inbox(&acct, &s.inbox.id).unwrap();
    assert_eq!(d.open().accounts()[0].inbox.as_deref(), Some(s.inbox.id.as_str()));
}

#[test]
fn a_new_account_key_is_kept() {
    let d = Device::default();
    let (mut v, acct, _, _) = signed_up(&d);
    v.change_account_key(&acct).unwrap();
    let words = v.commit_account_key(&acct).unwrap();
    assert_eq!(d.open().show_words(&acct).unwrap(), words);
    assert_eq!(d.open().accounts()[0].bundle_gen, 2);
}

#[test]
fn sign_out_and_forget_list_remove_what_was_stored() {
    let d = Device::default();
    let (mut v, acct, inbox, list) = signed_up(&d);
    v.forget_list(&acct, &list).unwrap();
    let after = d.open();
    assert_eq!(after.list_state(&acct, &list), Err(VaultError::UnknownList));
    assert!(after.list_state(&acct, &inbox).is_ok());

    v.sign_out(&acct).unwrap();
    assert!(d.open().accounts().is_empty());
    assert!(d.store.scan("").unwrap().is_empty());
    assert_eq!(d.keychain.get(&format!("account/{acct}")).unwrap(), None);
}

#[test]
fn a_damaged_log_is_dropped_and_a_lost_cache_key_clears_the_store() {
    let d = Device::default();
    let (_, acct, inbox, list) = signed_up(&d);
    let mut store = d.store.clone();
    let (name, _) = store.scan(&format!("log/{acct}/{list}/")).unwrap().remove(0);
    store.put(&name, b"garbage").unwrap();
    let after = d.open();
    assert_eq!(after.list_state(&acct, &list), Err(VaultError::UnknownList), "comes back from the server");
    assert!(after.list_state(&acct, &inbox).is_ok());
    assert!(store.scan(&format!("log/{acct}/{list}/")).unwrap().is_empty());

    let mut keychain = d.keychain.clone();
    keychain.delete("cache-key").unwrap();
    assert!(d.open().accounts().is_empty());
    assert!(store.scan("acct/").unwrap().is_empty());
}
