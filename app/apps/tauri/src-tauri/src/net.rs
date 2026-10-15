//! The `net` commands: the `Transport` (client-spec §5). All HTTP and
//! WebSocket traffic goes through here, which holds the session cookie (the
//! webview never sees it) and sends the one `Origin` the server accepts on
//! every request and the upgrade.
//!
//! The cookie jar is our own, keyed by the server's origin: the server's
//! session cookie is `Secure` (server/README.md), and a browser-style jar
//! would never send it to an `http://` server the user chose, such as a
//! local one. Cookies go only back to the origin that set them.

use crate::wire::{Error, Result};
use futures_util::{SinkExt, StreamExt};
use reqwest::header::{CONTENT_TYPE, COOKIE, HeaderMap, HeaderValue, ORIGIN, SET_COOKIE};
use serde::Serialize;
use std::collections::HashMap;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Arc, Mutex, PoisonError};
use tauri::State;
use tauri::ipc::Channel;
use tokio::sync::mpsc;
use tokio_tungstenite::tungstenite::Message;
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use url::Url;

/// The server's `ORIGIN` setting must equal this (server/README.md).
const APP_ORIGIN: &str = "tauri://localhost";

type Sockets = Arc<Mutex<HashMap<u32, mpsc::UnboundedSender<Message>>>>;

pub struct Net {
    client: reqwest::Client,
    jar: Jar,
    sockets: Sockets,
    next: AtomicU32,
}

impl Net {
    pub fn new() -> Self {
        let client = reqwest::Client::builder()
            // The OS lets Settings name this device in another device's session list.
            .user_agent(format!("runer-app/{} ({})", env!("CARGO_PKG_VERSION"), std::env::consts::OS))
            .build()
            .expect("HTTP client");
        Self { client, jar: Jar::default(), sockets: Arc::default(), next: AtomicU32::new(1) }
    }
}

/// Cookies by server origin (scheme, host, port), then by name. In memory:
/// after a restart the client signs in again with the key in the vault.
#[derive(Default)]
struct Jar(Mutex<HashMap<String, HashMap<String, String>>>);

impl Jar {
    /// The `Cookie` header for `url`'s origin, if we hold any.
    fn header(&self, url: &Url) -> Option<HeaderValue> {
        let jar = self.0.lock().unwrap_or_else(PoisonError::into_inner);
        let cookies = jar.get(&url.origin().ascii_serialization()).filter(|c| !c.is_empty())?;
        let mut pairs: Vec<String> = cookies.iter().map(|(k, v)| format!("{k}={v}")).collect();
        pairs.sort();
        HeaderValue::from_str(&pairs.join("; ")).ok()
    }

    /// Keeps the response's `Set-Cookie`s; an empty value or `Max-Age` of zero or less deletes.
    fn keep(&self, url: &Url, headers: &HeaderMap) {
        let mut jar = self.0.lock().unwrap_or_else(PoisonError::into_inner);
        let cookies = jar.entry(url.origin().ascii_serialization()).or_default();
        for sc in headers.get_all(SET_COOKIE).iter().filter_map(|v| v.to_str().ok()) {
            let mut parts = sc.split(';');
            let Some((name, value)) = parts.next().and_then(|p| p.split_once('=')) else { continue };
            let (name, value) = (name.trim(), value.trim());
            let expired = parts.any(|a| {
                a.trim().split_once('=').is_some_and(|(k, v)| {
                    k.trim().eq_ignore_ascii_case("max-age") && v.trim().parse::<i64>().is_ok_and(|n| n <= 0)
                })
            });
            if value.is_empty() || expired {
                cookies.remove(name);
            } else {
                cookies.insert(name.to_owned(), value.to_owned());
            }
        }
    }
}

fn network(e: impl std::fmt::Display) -> Error {
    Error::new("network", e.to_string())
}

/// `server` + `path`, for an http(s) server URL with no query or fragment and a path starting with `/`.
fn endpoint(server: &str, path: &str) -> Result<Url> {
    let base = Url::parse(server).map_err(|e| Error::new("bad-request", format!("server URL: {e}")))?;
    if !matches!(base.scheme(), "http" | "https") || base.query().is_some() || base.fragment().is_some() {
        return Err(Error::new("bad-request", "the server URL must be plain http(s)"));
    }
    if !path.starts_with('/') {
        return Err(Error::new("bad-request", "path must start with /"));
    }
    Url::parse(&format!("{}{path}", server.trim_end_matches('/'))).map_err(|e| Error::new("bad-request", e.to_string()))
}

#[derive(Serialize)]
pub struct HttpResponse {
    status: u16,
    headers: HashMap<String, String>,
    /// Parsed JSON, or absent when there is none.
    #[serde(skip_serializing_if = "Option::is_none")]
    body: Option<serde_json::Value>,
}

#[tauri::command]
pub async fn net_request(
    net: State<'_, Net>,
    server: String,
    method: String,
    path: String,
    body: Option<serde_json::Value>,
) -> Result<HttpResponse> {
    let url = endpoint(&server, &path)?;
    let method =
        reqwest::Method::from_bytes(method.as_bytes()).map_err(|e| Error::new("bad-request", e.to_string()))?;
    let mut req = net.client.request(method, url.clone()).header(ORIGIN, APP_ORIGIN);
    if let Some(c) = net.jar.header(&url) {
        req = req.header(COOKIE, c);
    }
    if let Some(b) = body {
        req = req.json(&b);
    }
    let res = req.send().await.map_err(network)?;
    net.jar.keep(&url, res.headers());
    let status = res.status().as_u16();
    let headers: HashMap<String, String> =
        res.headers().iter().filter_map(|(k, v)| Some((k.as_str().to_owned(), v.to_str().ok()?.to_owned()))).collect();
    let json = headers.get(CONTENT_TYPE.as_str()).is_some_and(|t| t.contains("application/json"));
    let bytes = res.bytes().await.map_err(network)?;
    let body = if json && !bytes.is_empty() { Some(serde_json::from_slice(&bytes).map_err(network)?) } else { None };
    Ok(HttpResponse { status, headers, body })
}

/// What the webview hears about a socket: each text frame, then its close.
#[derive(Clone, Serialize)]
#[serde(tag = "t", rename_all = "lowercase")]
pub enum SocketEvent {
    Message { text: String },
    Close { code: u16, reason: String },
}

/// Opens a WebSocket with our cookie and `Origin`; resolves once the upgrade succeeded.
#[tauri::command]
pub async fn net_socket_open(
    net: State<'_, Net>,
    server: String,
    path: String,
    events: Channel<SocketEvent>,
) -> Result<u32> {
    let http = endpoint(&server, &path)?;
    let mut ws = http.clone();
    ws.set_scheme(if http.scheme() == "https" { "wss" } else { "ws" }).map_err(|()| network("bad scheme"))?;
    let mut req = ws.as_str().into_client_request().map_err(network)?;
    req.headers_mut().insert(ORIGIN, HeaderValue::from_static(APP_ORIGIN));
    if let Some(c) = net.jar.header(&http) {
        req.headers_mut().insert(COOKIE, c);
    }
    let (socket, _) = tokio_tungstenite::connect_async(req).await.map_err(network)?;
    let (mut sink, mut stream) = socket.split();
    let (tx, mut rx) = mpsc::unbounded_channel::<Message>();
    let id = net.next.fetch_add(1, Ordering::Relaxed);
    net.sockets.lock().unwrap_or_else(PoisonError::into_inner).insert(id, tx);

    // Writer: ends when `net_socket_close` drops the sender, closing the socket.
    tauri::async_runtime::spawn(async move {
        while let Some(m) = rx.recv().await {
            if sink.send(m).await.is_err() {
                return;
            }
        }
        let _ = sink.close().await;
    });
    let sockets = net.sockets.clone();
    tauri::async_runtime::spawn(async move {
        let (code, reason) = loop {
            match stream.next().await {
                Some(Ok(Message::Text(t))) => {
                    if events.send(SocketEvent::Message { text: t.to_string() }).is_err() {
                        break (1001, "webview gone".to_owned());
                    }
                }
                Some(Ok(Message::Close(f))) => {
                    break f.map(|f| (u16::from(f.code), f.reason.to_string())).unwrap_or((1005, String::new()));
                }
                Some(Ok(_)) => {}
                Some(Err(e)) => break (1006, e.to_string()),
                None => break (1006, String::new()),
            }
        };
        sockets.lock().unwrap_or_else(PoisonError::into_inner).remove(&id);
        let _ = events.send(SocketEvent::Close { code, reason });
    });
    Ok(id)
}

#[tauri::command]
pub async fn net_socket_send(net: State<'_, Net>, id: u32, text: String) -> Result<()> {
    let sockets = net.sockets.lock().unwrap_or_else(PoisonError::into_inner);
    let tx = sockets.get(&id).ok_or_else(|| network("socket closed"))?;
    tx.send(Message::text(text)).map_err(|_| network("socket closed"))
}

#[tauri::command]
pub async fn net_socket_close(net: State<'_, Net>, id: u32) -> Result<()> {
    net.sockets.lock().unwrap_or_else(PoisonError::into_inner).remove(&id);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_jar_returns_secure_cookies_to_their_own_origin_only() {
        let jar = Jar::default();
        let server = Url::parse("http://localhost:8080/v1/auth/login").unwrap();
        let mut h = HeaderMap::new();
        h.append(SET_COOKIE, HeaderValue::from_static("session=abc; Path=/; HttpOnly; Secure; SameSite=Strict"));
        h.append(SET_COOKIE, HeaderValue::from_static("other=1"));
        jar.keep(&server, &h);
        let sync = Url::parse("http://localhost:8080/v1/sync").unwrap();
        assert_eq!(jar.header(&sync).unwrap(), "other=1; session=abc");
        assert!(jar.header(&Url::parse("http://localhost:9090/").unwrap()).is_none());
        assert!(jar.header(&Url::parse("https://localhost:8080/").unwrap()).is_none());

        let mut gone = HeaderMap::new();
        gone.append(SET_COOKIE, HeaderValue::from_static("session=; Max-Age=0"));
        gone.append(SET_COOKIE, HeaderValue::from_static("other=1; Max-Age=-1"));
        jar.keep(&server, &gone);
        assert!(jar.header(&sync).is_none());
    }

    #[test]
    fn endpoints_join_the_server_and_path_and_refuse_odd_urls() {
        assert_eq!(endpoint("http://localhost:8080", "/v1/sync").unwrap().as_str(), "http://localhost:8080/v1/sync");
        assert_eq!(endpoint("https://x.test/api/", "/v1/a?b=1").unwrap().as_str(), "https://x.test/api/v1/a?b=1");
        assert!(endpoint("file:///etc", "/x").is_err());
        assert!(endpoint("https://x.test#frag", "/x").is_err());
        assert!(endpoint("https://x.test", "x").is_err());
    }
}
