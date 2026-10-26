//! The `wake` commands. iOS wakes the app now and then for a background
//! refresh (a `BGAppRefreshTask`); Rust emits `runer://wake`, the webview
//! syncs as it would in the foreground and calls `wake_done`. Elsewhere the
//! event never comes and the commands do nothing.

/// The event that tells the webview iOS woke the app.
#[cfg_attr(not(target_os = "ios"), allow(dead_code))]
const WAKE_EVENT: &str = "runer://wake";

/// Whether a wake came that the webview hasn't seen yet. iOS may launch the
/// app for one before the webview listens, so it asks once it does.
#[tauri::command]
pub fn wake_pending() -> bool {
    #[cfg(target_os = "ios")]
    return ios::PENDING.swap(false, std::sync::atomic::Ordering::SeqCst);
    #[cfg(not(target_os = "ios"))]
    false
}

/// The webview is done with the wake: iOS may suspend the app again.
#[tauri::command]
pub fn wake_done() {
    #[cfg(target_os = "ios")]
    ios::finish(None, true, "done");
}

#[cfg(target_os = "ios")]
pub use ios::{register, schedule};

#[cfg(target_os = "ios")]
mod ios {
    use block2::RcBlock;
    use objc2::rc::Retained;
    use objc2::runtime::AnyObject;
    use objc2::{class, msg_send};
    use objc2_foundation::{NSDate, NSError, NSString};
    use std::ptr::NonNull;
    use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
    use std::sync::{Mutex, OnceLock};
    use std::time::Duration;
    use tauri::{AppHandle, Emitter};

    /// Also in `BGTaskSchedulerPermittedIdentifiers` in `Info.ios.plist`.
    const TASK_ID: &str = "app.runer.refresh";
    /// iOS gives a refresh about 30 s; past this the task ends unfinished.
    const TIMEOUT: Duration = Duration::from_secs(25);
    /// The earliest the next refresh may come, in seconds.
    const INTERVAL: f64 = 15.0 * 60.0;

    static APP: OnceLock<AppHandle> = OnceLock::new();
    pub(super) static PENDING: AtomicBool = AtomicBool::new(false);
    /// Numbers the runs, so a run's timeout can't end a later one.
    static RUNS: AtomicU64 = AtomicU64::new(0);
    static TASK: Mutex<Option<(u64, Task)>> = Mutex::new(None);

    /// A `BGTask`, which may be completed from any thread.
    struct Task(Retained<AnyObject>);
    unsafe impl Send for Task {}

    fn scheduler() -> Retained<AnyObject> {
        unsafe { msg_send![class!(BGTaskScheduler), sharedScheduler] }
    }

    /// Registers the refresh and asks for the first one. Apple wants it
    /// before launch ends: `setup` runs inside `didFinishLaunching`.
    pub fn register(app: &AppHandle) {
        let _ = APP.set(app.clone());
        let handler = RcBlock::new(|task: NonNull<AnyObject>| run(unsafe { Retained::retain(task.as_ptr()) }));
        let id = NSString::from_str(TASK_ID);
        let queue: *const AnyObject = std::ptr::null();
        let ok: bool = unsafe {
            msg_send![&*scheduler(), registerForTaskWithIdentifier: &*id, usingQueue: queue, launchHandler: &*handler]
        };
        crate::debuglog::log(if ok { "refresh: registered" } else { "refresh: registering failed" });
        schedule();
    }

    /// Asks for the next refresh. The Simulator always refuses.
    pub fn schedule() {
        let id = NSString::from_str(TASK_ID);
        let result: Result<(), Retained<NSError>> = unsafe {
            let request: Retained<AnyObject> =
                msg_send![msg_send![class!(BGAppRefreshTaskRequest), alloc], initWithIdentifier: &*id];
            let _: () = msg_send![&*request, setEarliestBeginDate: &*NSDate::dateWithTimeIntervalSinceNow(INTERVAL)];
            msg_send![&*scheduler(), submitTaskRequest: &*request, error: _]
        };
        match result {
            Ok(()) => crate::debuglog::log("refresh: next one requested"),
            Err(e) => crate::debuglog::log(&format!("refresh: request refused: {}", e.localizedDescription())),
        }
    }

    /// iOS runs the refresh: hand it to the webview, and end it if the webview doesn't.
    fn run(task: Option<Retained<AnyObject>>) {
        let Some(task) = task else { return };
        let n = RUNS.fetch_add(1, Ordering::SeqCst) + 1;
        crate::debuglog::log(&format!("refresh: iOS started run {n}"));
        let expired = RcBlock::new(move || finish(Some(n), false, "expired by iOS"));
        let _: () = unsafe { msg_send![&*task, setExpirationHandler: &*expired] };
        if let Some((_, Task(t))) = TASK.lock().unwrap().replace((n, Task(task))) {
            let _: () = unsafe { msg_send![&*t, setTaskCompleted: false] };
        }
        std::thread::spawn(move || {
            std::thread::sleep(TIMEOUT);
            finish(Some(n), false, "timed out");
        });
        let Some(app) = APP.get() else { return finish(Some(n), false, "no app") };
        PENDING.store(true, Ordering::SeqCst);
        crate::debuglog::log(&match app.emit(super::WAKE_EVENT, ()) {
            Ok(()) => format!("refresh: run {n} handed to the webview"),
            Err(e) => format!("refresh: run {n} not handed to the webview: {e}"),
        });
    }

    /// Completes run `n` (any run if `None`), if it's still open, and asks for the next.
    /// A wake the webview never saw is dropped with it.
    pub(super) fn finish(n: Option<u64>, success: bool, why: &str) {
        let task = TASK.lock().unwrap().take_if(|(m, _)| n.is_none_or(|n| n == *m));
        let Some((m, Task(t))) = task else {
            if n.is_none() {
                crate::debuglog::log("refresh: webview done, no run open");
            }
            return;
        };
        crate::debuglog::log(&format!("refresh: run {m} ended: {why}"));
        PENDING.store(false, Ordering::SeqCst);
        let _: () = unsafe { msg_send![&*t, setTaskCompleted: success] };
        schedule();
    }
}
