//! The debug log: timestamped lines in `debug.log` in the app's data
//! directory, from Rust and the webview, shown under Settings. Only debug
//! builds write it, to see what happened while the app was in the
//! background. Lines name events and ids, never content.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::{Mutex, OnceLock};
use std::time::{SystemTime, UNIX_EPOCH};

/// Past this the older half is dropped.
const MAX_BYTES: u64 = 256 * 1024;

static FILE: OnceLock<PathBuf> = OnceLock::new();
static LOCK: Mutex<()> = Mutex::new(());

pub fn init(dir: &Path) {
    if cfg!(debug_assertions) {
        let _ = FILE.set(dir.join("debug.log"));
    }
}

/// Appends `<unix ms> <line>`; the UI shows the time in the device's zone.
pub fn log(line: &str) {
    let Some(path) = FILE.get() else { return };
    let _guard = LOCK.lock();
    let ms = SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_millis());
    if std::fs::metadata(path).is_ok_and(|m| m.len() > MAX_BYTES)
        && let Ok(text) = std::fs::read_to_string(path)
    {
        let half = text.len() / 2;
        // Bytes, not `str`: `half` may fall inside a character; just past a newline never does.
        let cut = text.as_bytes()[half..].iter().position(|&b| b == b'\n').map_or(text.len(), |i| half + i + 1);
        let _ = std::fs::write(path, &text[cut..]);
    }
    if let Ok(mut f) = std::fs::OpenOptions::new().create(true).append(true).open(path) {
        let _ = writeln!(f, "{ms} {}", line.replace('\n', " "));
    }
}

#[tauri::command]
pub fn debug_log_append(line: String) {
    log(&line);
}

/// The whole log; empty in release builds.
#[tauri::command]
pub fn debug_log_read() -> String {
    let _guard = LOCK.lock();
    FILE.get().and_then(|p| std::fs::read_to_string(p).ok()).unwrap_or_default()
}

#[tauri::command]
pub fn debug_log_clear() {
    let _guard = LOCK.lock();
    if let Some(p) = FILE.get() {
        let _ = std::fs::remove_file(p);
    }
}
