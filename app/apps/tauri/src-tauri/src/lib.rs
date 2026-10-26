//! The Tauri shell: the `Platform` capabilities behind
//! commands. Protocol logic in Rust is only the vault; sync, the list
//! document and the views stay in `client-core`.
//!
//! The webview gets exactly the commands listed in `build.rs` and the
//! `default` capability: no plugin commands, no filesystem, no shell.

mod debuglog;
mod device;
mod net;
mod notify;
mod store;
mod vault;
mod wake;
mod wire;

use tauri::{Emitter, Manager};
use tauri_plugin_deep_link::DeepLinkExt;

/// The event that carries an incoming `runer://` link to the webview.
const LINK_EVENT: &str = "runer://link";

/// The link the app was opened with, if any.
#[tauri::command]
fn links_initial(app: tauri::AppHandle) -> Option<String> {
    // A dev profile isn't the registered handler of `runer://`: hand it an invite link by hand.
    if let Some(link) = profile().and_then(|_| std::env::var("RUNER_LINK").ok()).filter(|l| !l.is_empty()) {
        return Some(link);
    }
    app.deep_link().get_current().ok().flatten()?.first().map(|u| u.to_string())
}

/// Signed updates, in builds that were given an update
/// feed and its key: the release workflow adds `plugins.updater` to the
/// config, so a build from a laptop never updates itself. Checked once at
/// start, from Rust: the webview gets no updater command.
#[cfg(desktop)]
fn update(app: &tauri::AppHandle) -> tauri::Result<()> {
    use tauri_plugin_updater::UpdaterExt;
    if !app.config().plugins.0.contains_key("updater") {
        return Ok(());
    }
    app.plugin(tauri_plugin_updater::Builder::new().build())?;
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        // Installs without asking. On macOS and Linux the new version runs from the next start; on Windows the
        // installer closes the app and opens it again.
        let done = async {
            match app.updater()?.check().await? {
                Some(update) => update.download_and_install(|_, _| {}, || {}).await,
                None => Ok(()),
            }
        };
        if let Err(e) = done.await {
            eprintln!("update: {e}");
        }
    });
    Ok(())
}

/// Dev only: `RUNER_PROFILE=anna` gives this copy of the app its
/// own data directory and keychain entries and lets it run beside another,
/// so two accounts can share a list on one machine. Ignored in release builds.
pub(crate) fn profile() -> Option<String> {
    if !cfg!(debug_assertions) {
        return None;
    }
    let name = std::env::var("RUNER_PROFILE").ok()?;
    (!name.is_empty() && name.chars().all(|c| c.is_ascii_alphanumeric())).then_some(name)
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let _ = rustls::crypto::ring::default_provider().install_default();
    #[cfg_attr(mobile, allow(unused_mut))]
    let mut builder = tauri::Builder::default();
    #[cfg(desktop)]
    if profile().is_none() {
        // First, so a second launch (e.g. from a link) hands over to the running app and exits.
        builder = builder.plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            if let Some(w) = app.get_webview_window("main") {
                let _ = w.unminimize();
                let _ = w.set_focus();
            }
        }));
    }
    builder
        .plugin(tauri_plugin_deep_link::init())
        .plugin(tauri_plugin_notification::init())
        .setup(|app| {
            let mut dir = app.path().app_data_dir()?;
            if let Some(profile) = profile() {
                dir = dir.join("profiles").join(profile);
            }
            std::fs::create_dir_all(&dir)?;
            debuglog::init(&dir);
            debuglog::log("app: started");
            let keychain = device::OsKeychain::new(&app.config().identifier);
            let vault = device::SqliteVaultStore::open(&dir.join("vault.sqlite"))
                .and_then(|s| runer_vault::Vault::open(Box::new(keychain), Box::new(s)).map_err(|e| e.to_string()));
            if let Err(e) = &vault {
                eprintln!("vault: {e}");
            }
            app.manage(vault::VaultState::new(vault));
            app.manage(store::LocalStore::open(&dir.join("store.sqlite"))?);
            app.manage(net::Net::new());
            #[cfg(desktop)]
            app.manage(notify::Reminders::default());
            #[cfg(desktop)]
            update(app.handle())?;
            #[cfg(target_os = "ios")]
            wake::register(app.handle());
            let handle = app.handle().clone();
            app.deep_link().on_open_url(move |event| {
                for url in event.urls() {
                    let _ = handle.emit(LINK_EVENT, url.to_string());
                }
            });
            Ok(())
        })
        .on_window_event(|_, _event| {
            #[cfg(mobile)]
            match _event {
                tauri::WindowEvent::Suspended => {
                    debuglog::log("app: to background");
                    // Ask again on leaving the foreground, so the next refresh comes a while after this sync.
                    #[cfg(target_os = "ios")]
                    wake::schedule();
                }
                tauri::WindowEvent::Resumed => debuglog::log("app: to foreground"),
                _ => {}
            }
        })
        .invoke_handler(tauri::generate_handler![
            vault::vault_accounts,
            vault::vault_set_inbox,
            vault::vault_sign_in,
            vault::vault_start_sign_up,
            vault::vault_solve_sign_up_pow,
            vault::vault_confirm_sign_up,
            vault::vault_sign_out,
            vault::vault_show_words,
            vault::vault_seal_account_settings,
            vault::vault_open_account_settings,
            vault::vault_sign_server_message,
            vault::vault_load_identity,
            vault::vault_change_account_key,
            vault::vault_commit_account_key,
            vault::vault_apply_log,
            vault::vault_list_state,
            vault::vault_check_head,
            vault::vault_forget_list,
            vault::vault_create_list,
            vault::vault_rotate,
            vault::vault_seal_content,
            vault::vault_open_contents,
            vault::vault_add_member,
            vault::vault_remove_member,
            vault::vault_leave,
            vault::vault_known_contacts,
            vault::vault_safety_code,
            vault::vault_create_invite,
            vault::vault_uninvite,
            vault::vault_open_invite_link,
            vault::vault_read_invite_package,
            vault::vault_join,
            store::store_get,
            store::store_range,
            store::store_write,
            net::net_request,
            net::net_socket_open,
            net::net_socket_send,
            net::net_socket_close,
            notify::notify_replace_all,
            notify::notify_permission,
            notify::notify_request_permission,
            wake::wake_pending,
            wake::wake_done,
            debuglog::debug_log_append,
            debuglog::debug_log_read,
            debuglog::debug_log_clear,
            links_initial,
        ])
        .run(tauri::generate_context!())
        .expect("error while running the app");
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeSet;

    /// A command is written in three places: the invoke handler above, `COMMANDS` in `build.rs` (which makes its
    /// permission) and the `default` capability (which grants it to the window). One that is missing from the last
    /// two compiles, and is then refused when the webview calls it: "not allowed. Command not found".
    #[test]
    fn every_command_is_declared_and_allowed() {
        // The first `open … close` after `start` in `src`, split at commas.
        fn items<'a>(src: &'a str, start: &str, open: &str, close: &str) -> BTreeSet<&'a str> {
            let from = src.find(start).expect("the list's start") + start.len();
            let from = from + src[from..].find(open).expect("the list's opening") + open.len();
            let to = from + src[from..].find(close).expect("the list's end");
            src[from..to].split(',').map(str::trim).filter(|s| !s.is_empty()).collect()
        }
        let handler: BTreeSet<String> = items(include_str!("lib.rs"), ".invoke_handler(", "generate_handler![", "]")
            .into_iter()
            .map(|path| path.rsplit("::").next().unwrap().to_owned())
            .collect();
        let declared: BTreeSet<String> = items(include_str!("../build.rs"), "const COMMANDS", "= &[", "]")
            .into_iter()
            .map(|quoted| quoted.trim_matches('"').to_owned())
            .collect();
        let capability: serde_json::Value = serde_json::from_str(include_str!("../capabilities/default.json")).unwrap();
        let permissions: Vec<&str> =
            capability["permissions"].as_array().unwrap().iter().map(|p| p.as_str().unwrap()).collect();
        let allowed: BTreeSet<String> =
            permissions.iter().filter_map(|p| p.strip_prefix("allow-")).map(|name| name.replace('-', "_")).collect();

        assert!(handler.len() > 40, "the handler's list wasn't read: {handler:?}");
        assert_eq!(handler, declared, "the invoke handler (left) and COMMANDS in build.rs (right)");
        assert_eq!(handler, allowed, "the invoke handler (left) and capabilities/default.json (right)");
        assert_eq!(
            permissions.len(),
            permissions.iter().collect::<BTreeSet<_>>().len(),
            "a permission is listed twice"
        );
    }
}
