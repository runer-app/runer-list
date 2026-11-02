//! Ed25519 edge cases with the verdicts of Go's `crypto/ed25519.Verify`,
//! which the server uses (`protocol/vectors/ed25519-edge.json` at the repo
//! root).
//! Client and server must accept exactly the same signatures, or a log entry
//! one side accepts could be refused by the other.

use runer_crypto::ed25519_verify;
use serde_json::Value;

#[test]
fn every_verdict_matches_go() {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../../protocol/vectors/ed25519-edge.json");
    let v: Value = serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap();
    let unhex = |c: &Value, k: &str| hex::decode(c[k].as_str().unwrap()).unwrap();
    let mut wrong = Vec::new();
    let cases = v["cases"].as_array().unwrap();
    for c in cases {
        let ours = ed25519_verify(&unhex(c, "pub"), &unhex(c, "msg"), &unhex(c, "sig"));
        if ours != c["valid"].as_bool().unwrap() {
            wrong.push(format!("{} (Go: {}, ours: {ours})", c["name"].as_str().unwrap(), c["valid"]));
        }
    }
    assert!(wrong.is_empty(), "{} of {} cases disagree with Go:\n{}", wrong.len(), cases.len(), wrong.join("\n"));
}
