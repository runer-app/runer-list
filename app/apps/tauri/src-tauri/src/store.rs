//! The `store` commands: `LocalStore` on its own SQLite
//! file. Only get, range and an all-or-nothing batch of writes; no SQL
//! reaches the webview. Everything stored is already ciphertext.

use crate::wire::{B64, Error, Result};
use rusqlite::{Connection, OptionalExtension, params};
use serde::Deserialize;
use std::path::Path;
use std::sync::{Mutex, MutexGuard, PoisonError};
use tauri::State;

pub struct LocalStore(Mutex<Connection>);

fn db(e: rusqlite::Error) -> Error {
    Error::new("storage", e.to_string())
}

impl LocalStore {
    pub fn open(path: &Path) -> rusqlite::Result<Self> {
        let c = Connection::open(path)?;
        c.execute_batch(
            "PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL;
             CREATE TABLE IF NOT EXISTS kv (tbl TEXT NOT NULL, key TEXT NOT NULL, value BLOB NOT NULL,
               PRIMARY KEY (tbl, key)) WITHOUT ROWID;",
        )?;
        Ok(Self(Mutex::new(c)))
    }

    fn lock(&self) -> MutexGuard<'_, Connection> {
        self.0.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn get(&self, table: &str, key: &str) -> Result<Option<B64>> {
        self.lock()
            .query_row("SELECT value FROM kv WHERE tbl = ?1 AND key = ?2", [table, key], |r| r.get::<_, Vec<u8>>(0))
            .optional()
            .map(|v| v.map(B64))
            .map_err(db)
    }

    fn range(&self, table: &str, prefix: &str, after: Option<&str>, limit: Option<u32>) -> Result<Vec<(String, B64)>> {
        let c = self.lock();
        let from = match after {
            Some(a) if a >= prefix => a,
            _ => prefix,
        };
        let op = if after.is_some_and(|a| a >= prefix) { ">" } else { ">=" };
        let mut st =
            c.prepare(&format!("SELECT key, value FROM kv WHERE tbl = ?1 AND key {op} ?2 ORDER BY key")).map_err(db)?;
        let rows =
            st.query_map([table, from], |r| Ok((r.get::<_, String>(0)?, r.get::<_, Vec<u8>>(1)?))).map_err(db)?;
        let mut out = Vec::new();
        for row in rows {
            let (k, v) = row.map_err(db)?;
            if !k.starts_with(prefix) || limit.is_some_and(|l| out.len() >= l as usize) {
                break;
            }
            out.push((k, B64(v)));
        }
        Ok(out)
    }

    fn write(&self, ops: Vec<Op>) -> Result<()> {
        let mut c = self.lock();
        let tx = c.transaction().map_err(db)?;
        for op in ops {
            match op {
                Op::Put { table, key, value } => tx.execute(
                    "INSERT OR REPLACE INTO kv (tbl, key, value) VALUES (?1, ?2, ?3)",
                    params![table, key, value.0],
                ),
                Op::Delete { table, key } => tx.execute("DELETE FROM kv WHERE tbl = ?1 AND key = ?2", [table, key]),
            }
            .map_err(db)?;
        }
        tx.commit().map_err(db)
    }
}

#[derive(Deserialize)]
#[serde(tag = "op", rename_all = "lowercase")]
pub enum Op {
    Put { table: String, key: String, value: B64 },
    Delete { table: String, key: String },
}

#[tauri::command]
pub async fn store_get(store: State<'_, LocalStore>, table: String, key: String) -> Result<Option<B64>> {
    store.get(&table, &key)
}

/// Entries of `table` whose key starts with `prefix`, in key order, after `after` and at most `limit`.
#[tauri::command]
pub async fn store_range(
    store: State<'_, LocalStore>,
    table: String,
    prefix: String,
    after: Option<String>,
    limit: Option<u32>,
) -> Result<Vec<(String, B64)>> {
    store.range(&table, &prefix, after.as_deref(), limit)
}

/// Applies every write, or none of them.
#[tauri::command]
pub async fn store_write(store: State<'_, LocalStore>, ops: Vec<Op>) -> Result<()> {
    store.write(ops)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn put(table: &str, key: &str) -> Op {
        Op::Put { table: table.into(), key: key.into(), value: B64(key.as_bytes().to_vec()) }
    }

    fn keys(r: Vec<(String, B64)>) -> Vec<String> {
        r.into_iter().map(|(k, _)| k).collect()
    }

    #[test]
    fn range_stays_in_the_prefix_and_table_and_honours_after_and_limit() {
        let s = LocalStore::open(Path::new(":memory:")).unwrap();
        s.write(vec![put("t", "a/1"), put("t", "a/2"), put("t", "a/3"), put("t", "b/1"), put("u", "a/9")]).unwrap();
        assert_eq!(keys(s.range("t", "a/", None, None).unwrap()), ["a/1", "a/2", "a/3"]);
        assert_eq!(keys(s.range("t", "a/", Some("a/1"), Some(1)).unwrap()), ["a/2"]);
        assert_eq!(keys(s.range("t", "a/", Some("0"), None).unwrap()), ["a/1", "a/2", "a/3"]);
    }

    #[test]
    fn a_batch_is_all_or_nothing() {
        let s = LocalStore::open(Path::new(":memory:")).unwrap();
        s.write(vec![put("t", "k")]).unwrap();
        s.lock()
            .execute_batch(
                "CREATE TRIGGER no_x BEFORE INSERT ON kv WHEN NEW.key = 'x' BEGIN SELECT RAISE(ABORT, 'no'); END;",
            )
            .unwrap();
        assert!(s.write(vec![Op::Delete { table: "t".into(), key: "k".into() }, put("t", "x")]).is_err());
        assert!(s.get("t", "k").unwrap().is_some());
    }
}
