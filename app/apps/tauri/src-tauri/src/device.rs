//! Where the vault keeps what survives a restart (client-spec §6.2, §9): the
//! OS keychain through `keyring`, and the vault's own SQLite file, which the
//! `store` commands can't open.

use runer_vault::storage::{Keychain, StorageError, Store};
use rusqlite::{Connection, OptionalExtension, params};
use std::path::Path;
use zeroize::Zeroizing;

/// macOS Keychain, Windows Credential Manager, Secret Service on Linux.
/// On Linux without a running keyring daemon every call fails, and the app
/// says so; there is no plaintext fallback. On iOS, the Keychain through the
/// Security framework, since `keyring` can't choose when an item may be read.
pub struct OsKeychain {
    /// The keychain service every entry is filed under.
    service: String,
}

impl OsKeychain {
    /// `identifier` is the app's own (`tauri.conf.json`), the one place it is written.
    pub fn new(identifier: &str) -> Self {
        // A dev profile keeps its own entries.
        Self { service: crate::profile().map_or(identifier.to_owned(), |p| format!("{identifier}.{p}")) }
    }

    #[cfg(not(target_os = "ios"))]
    fn entry(&self, name: &str) -> Result<keyring::Entry, StorageError> {
        keyring::Entry::new(&self.service, name).map_err(|e| format!("keychain: {e}"))
    }
}

/// Every item is `AfterFirstUnlockThisDeviceOnly`: readable once the phone
/// has been unlocked after a restart, and never part of a backup or a move
/// to another phone.
#[cfg(target_os = "ios")]
impl Keychain for OsKeychain {
    fn get(&self, name: &str) -> Result<Option<Zeroizing<Vec<u8>>>, StorageError> {
        use security_framework::passwords::{PasswordOptions, generic_password};
        match generic_password(PasswordOptions::new_generic_password(&self.service, name)) {
            Ok(s) => Ok(Some(Zeroizing::new(s))),
            Err(e) if e.code() == ERR_SEC_ITEM_NOT_FOUND => Ok(None),
            Err(e) => Err(format!("keychain: {e}")),
        }
    }

    /// Replaces the item, since an update would keep the class it was made
    /// with. If the write fails after the old item is gone, the secret is
    /// gone from this phone and the error says so: sign in again.
    fn set(&mut self, name: &str, secret: &[u8]) -> Result<(), StorageError> {
        use security_framework::access_control::{ProtectionMode, SecAccessControl};
        use security_framework::passwords::{PasswordOptions, set_generic_password_options};
        let access =
            SecAccessControl::create_with_protection(Some(ProtectionMode::AccessibleAfterFirstUnlockThisDeviceOnly), 0)
                .map_err(|e| format!("keychain: {e}"))?;
        self.delete(name)?;
        let mut options = PasswordOptions::new_generic_password(&self.service, name);
        options.set_access_control(access);
        set_generic_password_options(secret, options).map_err(|e| format!("keychain: {e}"))
    }

    fn delete(&mut self, name: &str) -> Result<(), StorageError> {
        match security_framework::passwords::delete_generic_password(&self.service, name) {
            Ok(()) => Ok(()),
            Err(e) if e.code() == ERR_SEC_ITEM_NOT_FOUND => Ok(()),
            Err(e) => Err(format!("keychain: {e}")),
        }
    }
}

/// `errSecItemNotFound`.
#[cfg(target_os = "ios")]
const ERR_SEC_ITEM_NOT_FOUND: i32 = -25300;

#[cfg(not(target_os = "ios"))]
impl Keychain for OsKeychain {
    fn get(&self, name: &str) -> Result<Option<Zeroizing<Vec<u8>>>, StorageError> {
        match self.entry(name)?.get_secret() {
            Ok(s) => Ok(Some(Zeroizing::new(s))),
            Err(keyring::Error::NoEntry) => Ok(None),
            Err(e) => Err(format!("keychain: {e}")),
        }
    }

    fn set(&mut self, name: &str, secret: &[u8]) -> Result<(), StorageError> {
        self.entry(name)?.set_secret(secret).map_err(|e| format!("keychain: {e}"))
    }

    fn delete(&mut self, name: &str) -> Result<(), StorageError> {
        match self.entry(name)?.delete_credential() {
            Ok(()) | Err(keyring::Error::NoEntry) => Ok(()),
            Err(e) => Err(format!("keychain: {e}")),
        }
    }
}

/// The vault's own store: one SQLite file of opaque values the vault sealed.
pub struct SqliteVaultStore(Connection);

fn db(e: rusqlite::Error) -> StorageError {
    format!("vault store: {e}")
}

impl SqliteVaultStore {
    pub fn open(path: &Path) -> Result<Self, StorageError> {
        let c = Connection::open(path).map_err(db)?;
        c.execute_batch(
            "PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL;
             CREATE TABLE IF NOT EXISTS vault (key TEXT PRIMARY KEY, value BLOB NOT NULL) WITHOUT ROWID;",
        )
        .map_err(db)?;
        Ok(Self(c))
    }
}

impl Store for SqliteVaultStore {
    fn get(&self, key: &str) -> Result<Option<Vec<u8>>, StorageError> {
        self.0.query_row("SELECT value FROM vault WHERE key = ?1", [key], |r| r.get(0)).optional().map_err(db)
    }

    fn put(&mut self, key: &str, value: &[u8]) -> Result<(), StorageError> {
        self.0.execute("INSERT OR REPLACE INTO vault (key, value) VALUES (?1, ?2)", params![key, value]).map_err(db)?;
        Ok(())
    }

    fn delete(&mut self, key: &str) -> Result<(), StorageError> {
        self.0.execute("DELETE FROM vault WHERE key = ?1", [key]).map_err(db)?;
        Ok(())
    }

    fn scan(&self, prefix: &str) -> Result<Vec<(String, Vec<u8>)>, StorageError> {
        let mut st = self.0.prepare("SELECT key, value FROM vault WHERE key >= ?1 ORDER BY key").map_err(db)?;
        let rows = st.query_map([prefix], |r| Ok((r.get::<_, String>(0)?, r.get::<_, Vec<u8>>(1)?))).map_err(db)?;
        let mut out = Vec::new();
        for row in rows {
            let (k, v) = row.map_err(db)?;
            if !k.starts_with(prefix) {
                break;
            }
            out.push((k, v));
        }
        Ok(out)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_vault_store_scans_by_prefix_in_key_order() {
        let mut s = SqliteVaultStore::open(Path::new(":memory:")).unwrap();
        for k in ["log/b/1", "acct/x", "log/a/2", "log/a/1", "logz"] {
            s.put(k, k.as_bytes()).unwrap();
        }
        let keys: Vec<String> = s.scan("log/").unwrap().into_iter().map(|(k, _)| k).collect();
        assert_eq!(keys, ["log/a/1", "log/a/2", "log/b/1"]);
        s.delete("log/a/1").unwrap();
        assert_eq!(s.get("log/a/1").unwrap(), None);
        assert_eq!(s.get("acct/x").unwrap().as_deref(), Some(&b"acct/x"[..]));
    }
}
