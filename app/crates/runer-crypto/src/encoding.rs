//! The text encodings that signed and sealed JSON uses: unpadded base64url,
//! lowercase hex, and Go's JSON string escaping, byte for byte as the Go
//! server writes and reads them.

use crate::primitives::{CryptoError, Result};

const B64: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";

pub fn encode_base64url(b: &[u8]) -> String {
    let mut out = String::with_capacity(b.len().div_ceil(3) * 4);
    for chunk in b.chunks(3) {
        let n = chunk.iter().enumerate().fold(0u32, |n, (i, &x)| n | (x as u32) << (16 - 8 * i));
        for i in 0..=chunk.len() {
            out.push(B64[(n >> (18 - 6 * i) & 63) as usize] as char);
        }
    }
    out
}

/// Strict unpadded base64url. The callers re-encode what they parse and
/// compare bytes, so a lenient decoder would change no verdict.
pub fn decode_base64url(s: &str) -> Result<Vec<u8>> {
    let bad = || CryptoError::new("invalid base64url");
    if s.len() % 4 == 1 {
        return Err(bad());
    }
    let mut out = Vec::with_capacity(s.len() * 3 / 4);
    let (mut acc, mut bits) = (0u32, 0);
    for c in s.bytes() {
        let v = B64.iter().position(|&x| x == c).ok_or_else(bad)? as u32;
        acc = acc << 6 | v;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
        }
    }
    if acc & ((1 << bits) - 1) != 0 {
        return Err(bad());
    }
    Ok(out)
}

/// Decodes exactly what Go's `base64.RawURLEncoding.DecodeString` accepts:
/// `\r` and `\n` ignored, non-zero trailing bits tolerated. Log entries are
/// decoded with this, so the vault and the Go validator read the same bytes
/// from the same string.
pub fn decode_base64url_go(s: &str) -> Result<Vec<u8>> {
    let clean: Vec<u8> = s.bytes().filter(|&c| c != b'\r' && c != b'\n').collect();
    if clean.len() % 4 == 1 {
        return Err(CryptoError::new("invalid base64url length"));
    }
    let mut out = Vec::with_capacity(clean.len() * 3 / 4);
    let (mut acc, mut bits) = (0u32, 0);
    for c in clean {
        let v = B64.iter().position(|&x| x == c).ok_or_else(|| CryptoError::new("invalid base64url character"))?;
        acc = (acc << 6 | v as u32) & 0xfff;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
        }
    }
    Ok(out)
}

pub fn encode_hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// Lowercase hex only, as list ids are written.
pub fn decode_hex_lower(s: &str) -> Option<Vec<u8>> {
    if !s.len().is_multiple_of(2) || !s.bytes().all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c)) {
        return None;
    }
    (0..s.len()).step_by(2).map(|i| u8::from_str_radix(&s[i..i + 2], 16).ok()).collect()
}

/// A JSON string exactly as Go's `encoding/json` writes it with HTML escaping off:
/// Go's escapes (`\b` and `\f` short forms since Go 1.22, U+2028/9 escaped),
/// with `<`, `>` and `&` left alone.
pub fn go_json_string(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for ch in s.chars() {
        match ch {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{08}' => out.push_str("\\b"),
            '\u{0c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn base64url_round_trips_every_length() {
        for n in 0..70u8 {
            let b: Vec<u8> = (0..n).map(|i| i.wrapping_mul(37)).collect();
            assert_eq!(decode_base64url(&encode_base64url(&b)).unwrap(), b);
        }
        assert!(decode_base64url("AB=").is_err());
        assert!(decode_base64url("A").is_err());
        assert!(decode_base64url("AB").is_err(), "non-zero trailing bits");
        assert!(decode_base64url("+/").is_err());
    }

    #[test]
    fn base64url_go_is_lenient_exactly_where_go_is() {
        assert_eq!(decode_base64url_go("Zm9vYmFy").unwrap(), b"foobar");
        assert_eq!(decode_base64url_go("AB").unwrap(), [0], "non-zero trailing bits accepted");
        assert_eq!(decode_base64url_go("AA\r\nAA").unwrap(), [0, 0, 0]);
        for bad in ["A", "AAAAA", "A=", "AA==", "A+/A", "a b"] {
            assert!(decode_base64url_go(bad).is_err(), "{bad}");
        }
        for n in 0..70u8 {
            let b: Vec<u8> = (0..n).map(|i| i.wrapping_mul(37)).collect();
            assert_eq!(decode_base64url_go(&encode_base64url(&b)).unwrap(), b);
        }
    }

    #[test]
    fn go_json_string_matches_go() {
        assert_eq!(go_json_string("a\"\\\u{8}\u{c}\n\r\t\u{1}<&>\u{2028}å"), r#""a\"\\\b\f\n\r\t\u0001<&>\u2028å""#);
    }
}
