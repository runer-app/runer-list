//! The `notify` commands: the `Notifier` (client-spec §7.2). The
//! notification plugin shows desktop notifications at once and ignores a
//! schedule (§6.2a), so on desktop we keep the schedule ourselves and show
//! each reminder when it's due, while the app runs. On a phone the OS
//! keeps the schedule, so a reminder fires with the app closed.

use crate::wire::{Error, Result};
use serde::Deserialize;
use std::time::{SystemTime, UNIX_EPOCH};
use tauri::{AppHandle, Runtime};
use tauri_plugin_notification::{NotificationExt, PermissionState};

/// Desktop: the task that shows the reminders when they're due.
#[cfg(desktop)]
#[derive(Default)]
pub struct Reminders(std::sync::Mutex<Option<tauri::async_runtime::JoinHandle<()>>>);

#[derive(Deserialize)]
pub struct Reminder {
    /// Unix milliseconds.
    at: u64,
    /// The device's UTC offset at `at`, in minutes. Only the webview knows the time zone.
    #[cfg_attr(not(target_os = "ios"), allow(dead_code))]
    offset: i32,
    title: String,
    body: Option<String>,
}

fn now_ms() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_millis() as u64)
}

/// The time to give the plugin so the reminder fires at `at`. The plugin's
/// iOS code reads the UTC time it is given as local time (§6.2a), so there
/// the time is shifted by the device's offset; elsewhere it is `at`.
#[cfg_attr(desktop, allow(dead_code))]
fn fire_time(at: u64, offset: i32) -> Option<time::OffsetDateTime> {
    let shift = if cfg!(target_os = "ios") { i128::from(offset) * 60_000 } else { 0 };
    time::OffsetDateTime::from_unix_timestamp_nanos((i128::from(at) + shift) * 1_000_000).ok()
}

/// Makes the scheduled set exactly `reminders`. Ones already due are dropped, not fired late.
#[cfg(mobile)]
#[tauri::command]
pub async fn notify_replace_all<R: Runtime>(app: AppHandle<R>, mut reminders: Vec<Reminder>) -> Result<()> {
    use tauri_plugin_notification::Schedule;
    let err = |e: tauri_plugin_notification::Error| Error::new("notify", e.to_string());
    reminders.retain(|r| r.at > now_ms());
    app.notification().cancel_all().map_err(err)?;
    for (id, r) in reminders.into_iter().enumerate() {
        let Some(date) = fire_time(r.at, r.offset) else { continue };
        let mut n = app.notification().builder().id(id as i32).title(r.title).schedule(Schedule::At {
            date,
            repeating: false,
            allow_while_idle: true,
        });
        if let Some(b) = r.body {
            n = n.body(b);
        }
        // One that came due while the others were being scheduled is refused; the rest still count.
        let _ = n.show();
    }
    Ok(())
}

/// Makes the scheduled set exactly `reminders`. Ones already due are dropped, not fired late.
#[cfg(desktop)]
#[tauri::command]
pub async fn notify_replace_all<R: Runtime>(
    app: AppHandle<R>,
    state: tauri::State<'_, Reminders>,
    mut reminders: Vec<Reminder>,
) -> Result<()> {
    use std::time::Duration;
    reminders.retain(|r| r.at > now_ms());
    reminders.sort_by_key(|r| r.at);
    let task = tauri::async_runtime::spawn(async move {
        for r in reminders {
            tokio::time::sleep(Duration::from_millis(r.at.saturating_sub(now_ms()))).await;
            let mut n = app.notification().builder().title(r.title);
            if let Some(b) = r.body {
                n = n.body(b);
            }
            let _ = n.show();
        }
    });
    if let Some(old) = state.0.lock().map_err(|e| Error::new("notify", e.to_string()))?.replace(task) {
        old.abort();
    }
    Ok(())
}

#[tauri::command]
pub async fn notify_permission<R: Runtime>(app: AppHandle<R>) -> Result<&'static str> {
    let p = app.notification().permission_state().map_err(|e| Error::new("notify", e.to_string()))?;
    Ok(state_name(p))
}

fn state_name(p: PermissionState) -> &'static str {
    match p {
        PermissionState::Granted => "granted",
        PermissionState::Denied => "denied",
        _ => "prompt",
    }
}

/// Asks the OS for permission (Settings → notifications); gives the state afterwards.
#[tauri::command]
pub async fn notify_request_permission<R: Runtime>(app: AppHandle<R>) -> Result<&'static str> {
    let p = app.notification().request_permission().map_err(|e| Error::new("notify", e.to_string()))?;
    Ok(state_name(p))
}

#[cfg(test)]
mod tests {
    use super::fire_time;

    #[test]
    fn the_plugin_gets_the_reminders_own_time_except_on_ios() {
        // 2026-10-08 12:00:00 UTC, on a device two hours ahead.
        let at = 1_791_460_800_000;
        let given = fire_time(at, 120).unwrap().unix_timestamp();
        let shift = if cfg!(target_os = "ios") { 7200 } else { 0 };
        assert_eq!(given, 1_791_460_800 + shift);
    }
}
