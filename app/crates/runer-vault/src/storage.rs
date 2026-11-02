//! Where the vault keeps what must survive a restart: the
//! OS keychain for the Account Keys and the device cache key, and the
//! vault's own store for everything else, sealed under the cache key.
//! The app implements both in Rust (`keyring`, SQLite); tests and the Node
//! add-on use the memory versions here.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex, PoisonError};
use zeroize::Zeroizing;

/// A failure of the keychain or store underneath the vault, as its message.
pub type StorageError = String;

/// Named secrets in the OS keychain.
pub trait Keychain: Send {
    fn get(&self, name: &str) -> Result<Option<Zeroizing<Vec<u8>>>, StorageError>;
    fn set(&mut self, name: &str, secret: &[u8]) -> Result<(), StorageError>;
    fn delete(&mut self, name: &str) -> Result<(), StorageError>;
}

/// The vault's own key-value store. Values are sealed by the vault; the
/// store sees opaque bytes under keys like `log/<account>/<list>/<seq>`.
pub trait Store: Send {
    fn get(&self, key: &str) -> Result<Option<Vec<u8>>, StorageError>;
    fn put(&mut self, key: &str, value: &[u8]) -> Result<(), StorageError>;
    fn delete(&mut self, key: &str) -> Result<(), StorageError>;
    /// Every entry whose key starts with `prefix`, in key order.
    fn scan(&self, prefix: &str) -> Result<Vec<(String, Vec<u8>)>, StorageError>;
}

type Shared = Arc<Mutex<BTreeMap<String, Vec<u8>>>>;

fn lock(m: &Shared) -> std::sync::MutexGuard<'_, BTreeMap<String, Vec<u8>>> {
    m.lock().unwrap_or_else(PoisonError::into_inner)
}

/// An in-memory keychain. Clones share their contents, so a test can open a
/// second vault on the same "device" to play a restart.
#[derive(Clone, Default)]
pub struct MemoryKeychain(Shared);

impl Keychain for MemoryKeychain {
    fn get(&self, name: &str) -> Result<Option<Zeroizing<Vec<u8>>>, StorageError> {
        Ok(lock(&self.0).get(name).map(|v| Zeroizing::new(v.clone())))
    }
    fn set(&mut self, name: &str, secret: &[u8]) -> Result<(), StorageError> {
        lock(&self.0).insert(name.to_owned(), secret.to_vec());
        Ok(())
    }
    fn delete(&mut self, name: &str) -> Result<(), StorageError> {
        lock(&self.0).remove(name);
        Ok(())
    }
}

/// An in-memory store; clones share their contents, like `MemoryKeychain`.
#[derive(Clone, Default)]
pub struct MemoryStore(Shared);

impl Store for MemoryStore {
    fn get(&self, key: &str) -> Result<Option<Vec<u8>>, StorageError> {
        Ok(lock(&self.0).get(key).cloned())
    }
    fn put(&mut self, key: &str, value: &[u8]) -> Result<(), StorageError> {
        lock(&self.0).insert(key.to_owned(), value.to_vec());
        Ok(())
    }
    fn delete(&mut self, key: &str) -> Result<(), StorageError> {
        lock(&self.0).remove(key);
        Ok(())
    }
    fn scan(&self, prefix: &str) -> Result<Vec<(String, Vec<u8>)>, StorageError> {
        Ok(lock(&self.0)
            .range(prefix.to_owned()..)
            .take_while(|(k, _)| k.starts_with(prefix))
            .map(|(k, v)| (k.clone(), v.clone()))
            .collect())
    }
}
