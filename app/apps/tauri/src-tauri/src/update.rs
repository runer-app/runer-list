//! Signed updates, in builds that were given an update feed and its key:
//! the release workflow adds `plugins.updater` to the config, so a build
//! from a laptop never updates itself. Checked once at start, from Rust. The
//! webview only hears that an update is ready and can ask to restart into
//! it (`update_restart`): it never names a version, a URL or the bytes.

// Off Windows, `Pending` is `()`.
#![cfg_attr(not(windows), allow(clippy::let_unit_value))]

use std::sync::{Mutex, PoisonError};

use tauri::{AppHandle, Manager};

#[cfg(desktop)]
use crate::debuglog::log;
use crate::wire::{Error, Result};

/// The event that tells the webview an update is ready: the window offers "Restart to update".
#[cfg(desktop)]
const READY_EVENT: &str = "runer://update-ready";

/// The downloaded update waiting for a restart. Windows keeps the verified
/// bytes, as its installer closes the app; macOS and Linux already
/// installed it.
#[cfg(windows)]
type Pending = (tauri_plugin_updater::Update, Vec<u8>);
#[cfg(not(windows))]
type Pending = ();

/// What went wrong, for the log: the errors' own text can hold paths.
#[cfg(desktop)]
fn failure(e: &tauri_plugin_updater::Error) -> &'static str {
    use tauri_plugin_updater::Error as E;
    match e {
        E::Reqwest(_) | E::Network(_) => "network",
        E::ReleaseNotFound | E::TargetNotFound(_) | E::TargetsNotFound(_) => "no release in the feed",
        E::Minisign(_) | E::Base64(_) | E::SignatureUtf8(_) => "bad signature",
        E::Io(_) => "disk",
        _ => "other",
    }
}

/// Managed only in builds with an update feed.
#[derive(Default)]
pub struct Ready(Mutex<Option<Pending>>);

#[cfg(desktop)]
pub fn start(app: &AppHandle) -> tauri::Result<()> {
    use tauri::Emitter;
    use tauri_plugin_updater::UpdaterExt;
    if !app.config().plugins.0.contains_key("updater") {
        log("update: no feed in this build");
        return Ok(());
    }
    app.plugin(tauri_plugin_updater::Builder::new().build())?;
    app.manage(Ready::default());
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        let done = async {
            log("update: checking");
            let Some(update) = app.updater()?.check().await? else {
                log("update: up to date");
                return Ok(());
            };
            log(&format!("update: {} found, downloading", update.version));
            // `download` checks the signature against the key in the config: nothing unsigned gets past it.
            let bytes = update.download(|_, _| {}, || {}).await?;
            // macOS and Linux swap the app on disk now; the running one is unaffected and the new one runs from
            // the next start. Windows waits for the click.
            #[cfg(not(windows))]
            let pending = update.install(bytes)?;
            #[cfg(windows)]
            let pending = (update, bytes);
            *app.state::<Ready>().0.lock().unwrap_or_else(PoisonError::into_inner) = Some(pending);
            log("update: ready");
            app.emit(READY_EVENT, ())?;
            Ok::<_, tauri_plugin_updater::Error>(())
        };
        if let Err(e) = done.await {
            log(&format!("update: failed: {}", failure(&e)));
            eprintln!("update: {e}");
        }
    });
    Ok(())
}

/// "Restart to update", refused unless an update is ready. Windows
/// installs it now (the installer closes the app and opens the new one);
/// the other systems restart into the version already on disk.
#[tauri::command]
pub fn update_restart(app: AppHandle) -> Result<()> {
    let pending = app
        .try_state::<Ready>()
        .and_then(|r| r.0.lock().unwrap_or_else(PoisonError::into_inner).take())
        .ok_or_else(|| Error::new("update", "no update is ready"))?;
    #[cfg(windows)]
    {
        let (update, bytes) = pending;
        update.install(bytes).map_err(|e| {
            log(&format!("update: installing failed: {}", failure(&e)));
            Error::new("update", e.to_string())
        })?;
    }
    #[cfg(not(windows))]
    let () = pending;
    app.restart()
}
