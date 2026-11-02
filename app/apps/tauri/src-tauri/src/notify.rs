//! The `notify` commands: the `Notifier`. The
//! notification plugin shows desktop notifications at once and ignores a
//! schedule (§6.2a), so on desktop we keep the schedule ourselves and show
//! each reminder when it's due, while the app runs. On a phone the OS
//! keeps the schedule, so a reminder fires with the app closed. Each step
//! goes to the device's log, so a missed reminder shows where it stopped.

use crate::debuglog::log;
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

/// Lines hold counts and durations only: never a title, a list or an absolute time.
fn log_received(received: usize, kept: usize) {
    log(&format!("reminders: {received} received, {} dropped as already past", received - kept));
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

/// iOS keeps at most 64 pending notifications; past that it drops the rest. The nearest ones are scheduled.
#[cfg(mobile)]
const MAX_PENDING: usize = 64;

/// Makes the scheduled set exactly `reminders`. Ones already due are dropped, not fired late.
#[cfg(mobile)]
#[tauri::command]
pub async fn notify_replace_all<R: Runtime>(app: AppHandle<R>, mut reminders: Vec<Reminder>) -> Result<()> {
    use tauri_plugin_notification::Schedule;
    let err = |e: tauri_plugin_notification::Error| Error::new("notify", e.to_string());
    let received = reminders.len();
    reminders.retain(|r| r.at > now_ms());
    log_received(received, reminders.len());
    reminders.sort_by_key(|r| r.at);
    // Not `cancel_all()`: it sends no arguments, which the iOS plugin can't decode. Not `pending()` either: the iOS
    // plugin leaves out `schedule`, which the Rust side requires, so it fails once anything is scheduled. Our ids are
    // 0..MAX_PENDING, so cancel those.
    app.notification().cancel((0..MAX_PENDING as i32).collect()).map_err(err)?;
    let (mut scheduled, mut refused) = (0, 0);
    for (id, r) in reminders.into_iter().take(MAX_PENDING).enumerate() {
        let Some(date) = fire_time(r.at, r.offset) else {
            refused += 1;
            continue;
        };
        // `at` goes along so `log_delivered` can tell how late the OS was; it stays on the device.
        let mut n = app
            .notification()
            .builder()
            .id(id as i32)
            .title(r.title)
            .extra("at", r.at.to_string())
            .schedule(Schedule::At { date, repeating: false, allow_while_idle: true });
        if let Some(b) = r.body {
            n = n.body(b);
        }
        // One that came due while the others were being scheduled is refused; the rest still count.
        if n.show().is_ok() {
            scheduled += 1;
        } else {
            refused += 1;
        }
    }
    log(&format!("reminders: {scheduled} scheduled with the OS, {refused} refused"));
    Ok(())
}

/// iOS shows a reminder with the app closed, so the app can't see it fire. What it can see, next time it runs, is
/// what iOS delivered and when, unless the person has dismissed it. Each is logged once: `debug.seen` keeps the newest
/// delivery logged. iOS doesn't say whether the app was in the foreground; `ago` places it among the log's other lines.
#[cfg(target_os = "ios")]
pub fn log_delivered() {
    use block2::RcBlock;
    use objc2::rc::Retained;
    use objc2::runtime::AnyObject;
    use objc2::{class, msg_send};
    use objc2_foundation::{NSDate, NSString};
    use std::ptr::NonNull;

    let Some(seen_file) = crate::debuglog::sidecar("seen") else { return };
    let done = RcBlock::new(move |delivered: NonNull<AnyObject>| {
        let delivered = delivered.as_ptr();
        let seen: u64 = std::fs::read_to_string(&seen_file).ok().and_then(|s| s.parse().ok()).unwrap_or(0);
        let mut newest = seen;
        let count: usize = unsafe { msg_send![delivered, count] };
        for i in 0..count {
            let (fired, at) = unsafe {
                let n: *mut AnyObject = msg_send![delivered, objectAtIndex: i];
                let date: Retained<NSDate> = msg_send![n, date];
                let request: *mut AnyObject = msg_send![n, request];
                let content: *mut AnyObject = msg_send![request, content];
                let info: *mut AnyObject = msg_send![content, userInfo];
                let extra: *mut AnyObject = msg_send![info, objectForKey: &*NSString::from_str("__EXTRA__")];
                let at: Option<Retained<NSString>> =
                    if extra.is_null() { None } else { msg_send![extra, objectForKey: &*NSString::from_str("at")] };
                ((date.timeIntervalSince1970() * 1000.0) as u64, at.and_then(|a| a.to_string().parse::<u64>().ok()))
            };
            let Some(at) = at.filter(|_| fired > seen) else { continue };
            newest = newest.max(fired);
            log(&format!(
                "reminders: iOS showed one {} ms late, {} s ago (it doesn't say if the app was open)",
                fired.saturating_sub(at),
                now_ms().saturating_sub(fired) / 1000
            ));
        }
        if newest > seen {
            let _ = std::fs::write(&seen_file, newest.to_string());
        }
    });
    unsafe {
        let center: Retained<AnyObject> = msg_send![class!(UNUserNotificationCenter), currentNotificationCenter];
        let _: () = msg_send![&*center, getDeliveredNotificationsWithCompletionHandler: &*done];
    }
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
    use tauri::Manager;
    let received = reminders.len();
    reminders.retain(|r| r.at > now_ms());
    log_received(received, reminders.len());
    reminders.sort_by_key(|r| r.at);
    if let Some(first) = reminders.first() {
        let next = first.at.saturating_sub(now_ms()) / 1000;
        log(&format!("reminders: timer running for {}, next in {next} s", reminders.len()));
    }
    let task = tauri::async_runtime::spawn(async move {
        for r in reminders {
            tokio::time::sleep(Duration::from_millis(r.at.saturating_sub(now_ms()))).await;
            let late = now_ms().saturating_sub(r.at);
            let focused = app.get_webview_window("main").is_some_and(|w| w.is_focused().unwrap_or(false));
            let mut n = app.notification().builder().title(r.title);
            if let Some(b) = r.body {
                n = n.body(b);
            }
            // `show()` returning Ok proves only that it was passed on: the plugin drops the delivery's result.
            let sent = if n.show().is_ok() {
                "passed to the OS, not confirmed shown"
            } else {
                "could not be passed to the OS"
            };
            let window = if focused { "app focused" } else { "app not focused" };
            log(&format!("reminders: one due {late} ms ago {sent} ({window})"));
        }
    });
    if let Some(old) = state.0.lock().map_err(|e| Error::new("notify", e.to_string()))?.replace(task) {
        if !old.inner().is_finished() {
            log("reminders: new plan replaced a timer that still had reminders waiting");
        }
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
