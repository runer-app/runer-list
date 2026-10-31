//! The device's log: timestamped lines in `debug.log` in the app's data
//! directory, from Rust and the webview, shown under Settings → Diagnostics.
//! Every build writes it, to see what the app did while nobody watched. It
//! stays on the device and is cleared on sign-out.
//!
//! Lines name events, counts and durations, never content. Whoever reads a
//! log may also run the server, so ids don't go in as they are: `log` turns
//! anything shaped like one into a label that means something only within
//! this log (`list-1`). The ids behind the labels are in `debug.ids`, which
//! goes with the log.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard, OnceLock, PoisonError};
use std::time::{SystemTime, UNIX_EPOCH};

/// Past this the older half is dropped.
const MAX_BYTES: u64 = 256 * 1024;
/// List ids are 64 hex characters, invite keys 43 of base64url. The longest word or code in a line is shorter or has
/// no digit or capital.
const MIN_ID_LEN: usize = 20;

static FILE: OnceLock<PathBuf> = OnceLock::new();
/// The ids seen so far, label `list-1` first. Also what keeps writers apart.
static IDS: Mutex<Vec<String>> = Mutex::new(Vec::new());

fn ids() -> MutexGuard<'static, Vec<String>> {
    IDS.lock().unwrap_or_else(PoisonError::into_inner)
}

pub fn init(dir: &Path) {
    let path = dir.join("debug.log");
    *ids() =
        std::fs::read_to_string(path.with_extension("ids")).unwrap_or_default().lines().map(str::to_owned).collect();
    let _ = FILE.set(path);
}

/// A file beside the log, for what has to outlive the app but isn't a line.
#[cfg(target_os = "ios")]
pub fn sidecar(extension: &str) -> Option<PathBuf> {
    FILE.get().map(|p| p.with_extension(extension))
}

/// Logs where a panic happened, not its message, which can hold anything. The usual hook still runs.
pub fn catch_panics() {
    let usual = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        let place = info.location().map_or_else(String::new, |l| {
            let file = Path::new(l.file()).file_name().map_or_else(Default::default, |f| f.to_string_lossy());
            format!(" at {file}:{}", l.line())
        });
        log(&format!("panic{place}"));
        usual(info);
    }));
}

/// `line` with every id replaced by its label; an id not seen before is added to `ids`.
fn labelled(line: &str, ids: &mut Vec<String>) -> String {
    let in_token = |c: char| c.is_ascii_alphanumeric() || c == '-' || c == '_';
    let mut out = String::with_capacity(line.len());
    for part in line.split_inclusive(|c| !in_token(c)) {
        let token = part.trim_end_matches(|c| !in_token(c));
        if token.len() >= MIN_ID_LEN && token.bytes().any(|b| b.is_ascii_digit() || b.is_ascii_uppercase()) {
            let at = ids.iter().position(|id| id == token).unwrap_or_else(|| {
                ids.push(token.to_owned());
                ids.len() - 1
            });
            out.push_str(&format!("list-{}", at + 1));
            out.push_str(&part[token.len()..]);
        } else {
            out.push_str(part);
        }
    }
    out
}

/// Appends `<unix ms> <line>`; the UI shows the time in the device's zone.
pub fn log(line: &str) {
    let Some(path) = FILE.get() else { return };
    let mut ids = ids();
    let known = ids.len();
    let line = labelled(&line.replace('\n', " "), &mut ids);
    if ids.len() > known
        && let Ok(mut f) = std::fs::OpenOptions::new().create(true).append(true).open(path.with_extension("ids"))
    {
        let _ = writeln!(f, "{}", ids[known..].join("\n"));
    }
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
        let _ = writeln!(f, "{ms} {line}");
    }
}

#[tauri::command]
pub fn debug_log_append(line: String) {
    log(&line);
}

/// The whole log.
#[tauri::command]
pub fn debug_log_read() -> String {
    let _guard = ids();
    FILE.get().and_then(|p| std::fs::read_to_string(p).ok()).unwrap_or_default()
}

#[tauri::command]
pub fn debug_log_clear() {
    let mut ids = ids();
    ids.clear();
    if let Some(p) = FILE.get() {
        let _ = std::fs::remove_file(p);
        let _ = std::fs::remove_file(p.with_extension("ids"));
    }
}

#[cfg(test)]
mod tests {
    use super::labelled;

    const LIST: &str = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08";
    const OTHER: &str = "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae";

    #[test]
    fn ids_become_the_same_label_each_time_and_the_rest_stays() {
        let mut ids = Vec::new();
        assert_eq!(
            labelled(&format!("sync: {LIST} caught up to 12, then {OTHER}, {LIST}."), &mut ids),
            "sync: list-1 caught up to 12, then list-2, list-1."
        );
        assert_eq!(ids, [LIST, OTHER]);
    }

    #[test]
    fn an_invite_key_is_labelled_too() {
        let mut ids = Vec::new();
        let line = labelled("join: Zm9vYmFy-_Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9v failed", &mut ids);
        assert_eq!(line, "join: list-1 failed");
    }

    #[test]
    fn words_codes_and_numbers_are_left_alone() {
        let mut ids = Vec::new();
        let line = "sync: invalid_credentials not-member-of-this-list-at-all in 1234 ms, version 0.1.5";
        assert_eq!(labelled(line, &mut ids), line);
        assert!(ids.is_empty());
    }
}
