//! The one canonical encoding of a log entry (E2EE §4.1). Byte for byte what
//! Go's `encoding/json` produces for `wireEntry` with HTML escaping off, so
//! "re-encode and compare" gives both validators the same verdict.

use super::error::LogError;
use runer_crypto::encoding::go_json_string;
use serde_json::{Map, Value};

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct WireEnvelope {
    pub recipient: String,
    pub hpke_ciphertext: String,
}

/// The JSON fields of an entry as text. An empty string, an empty list and
/// a zero `expires` mean "omitted". Integers are wide enough to hold any
/// value JSON gives us, so the range check comes after the canonical check,
/// as in Go.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct WireEntry {
    pub v: i128,
    pub group: String,
    pub seq: i128,
    pub prev: String,
    pub r#type: String,
    pub author: String,
    pub author_agree: String,
    pub subject: String,
    pub subject_agree: String,
    pub epoch: i128,
    pub envelopes: Vec<WireEnvelope>,
    pub key_chain: String,
    pub invite: String,
    /// Unix milliseconds; 0 = absent (Go's `omitempty`).
    pub expires: i128,
    pub invite_proof: String,
    /// Unix milliseconds.
    pub ts: i128,
}

/// Canonical bytes of `w`: fixed key order, no whitespace, empty fields omitted.
pub fn encode_wire_entry(w: &WireEntry) -> Vec<u8> {
    let mut s = format!("{{\"v\":{}", w.v);
    let opt = |s: &mut String, key: &str, value: &str| {
        if !value.is_empty() {
            s.push_str(&format!(",\"{key}\":{}", go_json_string(value)));
        }
    };
    opt(&mut s, "group", &w.group);
    s.push_str(&format!(",\"seq\":{}", w.seq));
    opt(&mut s, "prev", &w.prev);
    s.push_str(&format!(",\"type\":{},\"author\":{}", go_json_string(&w.r#type), go_json_string(&w.author)));
    opt(&mut s, "author_agree", &w.author_agree);
    opt(&mut s, "subject", &w.subject);
    opt(&mut s, "subject_agree", &w.subject_agree);
    s.push_str(&format!(",\"epoch\":{}", w.epoch));
    if !w.envelopes.is_empty() {
        let envs: Vec<String> = w
            .envelopes
            .iter()
            .map(|e| {
                format!(
                    "{{\"recipient\":{},\"hpke_ciphertext\":{}}}",
                    go_json_string(&e.recipient),
                    go_json_string(&e.hpke_ciphertext)
                )
            })
            .collect();
        s.push_str(&format!(",\"envelopes\":[{}]", envs.join(",")));
    }
    opt(&mut s, "key_chain", &w.key_chain);
    opt(&mut s, "invite", &w.invite);
    if w.expires != 0 {
        s.push_str(&format!(",\"expires\":{}", w.expires));
    }
    opt(&mut s, "invite_proof", &w.invite_proof);
    s.push_str(&format!(",\"ts\":{}}}", w.ts));
    s.into_bytes()
}

fn malformed(detail: impl Into<String>) -> LogError {
    LogError::malformed(detail)
}

/// Absent and `null` read as the zero value, like Go's `encoding/json`.
fn str(o: &Map<String, Value>, key: &str) -> Result<String, LogError> {
    match o.get(key) {
        None | Some(Value::Null) => Ok(String::new()),
        Some(Value::String(s)) => Ok(s.clone()),
        Some(_) => Err(malformed(format!("{key} must be a string"))),
    }
}

fn int(o: &Map<String, Value>, key: &str) -> Result<i128, LogError> {
    match o.get(key) {
        None | Some(Value::Null) => Ok(0),
        Some(Value::Number(n)) => n
            .as_i64()
            .map(i128::from)
            .or_else(|| n.as_u64().map(i128::from))
            .ok_or_else(|| malformed(format!("{key} must be an integer"))),
        Some(_) => Err(malformed(format!("{key} must be an integer"))),
    }
}

/// Reads the JSON the way Go's `json.Unmarshal` into `wireEntry` does, known
/// keys only. Whatever it reads loosely (unknown or duplicate keys, other
/// key spellings, whitespace, number forms) fails the canonical check after.
pub fn read_wire(raw: &[u8]) -> Result<WireEntry, LogError> {
    let json: Value = serde_json::from_slice(raw).map_err(|e| malformed(e.to_string()))?;
    let Value::Object(o) = json else {
        return Err(malformed("entry is not a JSON object"));
    };
    let envelopes = match o.get("envelopes") {
        None | Some(Value::Null) => Vec::new(),
        Some(Value::Array(envs)) => envs
            .iter()
            .map(|e| match e {
                Value::Object(e) => {
                    Ok(WireEnvelope { recipient: str(e, "recipient")?, hpke_ciphertext: str(e, "hpke_ciphertext")? })
                }
                _ => Err(malformed("envelope must be an object")),
            })
            .collect::<Result<_, _>>()?,
        Some(_) => return Err(malformed("envelopes must be an array")),
    };
    Ok(WireEntry {
        v: int(&o, "v")?,
        group: str(&o, "group")?,
        seq: int(&o, "seq")?,
        prev: str(&o, "prev")?,
        r#type: str(&o, "type")?,
        author: str(&o, "author")?,
        author_agree: str(&o, "author_agree")?,
        subject: str(&o, "subject")?,
        subject_agree: str(&o, "subject_agree")?,
        epoch: int(&o, "epoch")?,
        envelopes,
        key_chain: str(&o, "key_chain")?,
        invite: str(&o, "invite")?,
        expires: int(&o, "expires")?,
        invite_proof: str(&o, "invite_proof")?,
        ts: int(&o, "ts")?,
    })
}
