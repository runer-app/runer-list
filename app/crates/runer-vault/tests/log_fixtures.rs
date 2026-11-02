//! The shared log fixtures (`protocol/log-fixtures`, made by the server), replayed the
//! way Go's `TestFixtures` does: earlier entries must be good, and the
//! verdict on the last entry must equal `valid`.

mod common;

use common::{fixtures_dir, std_base64};
use runer_vault::log::{LogErrorCode as C, LogState, SignedEntry, apply_entries, parse_entry, validate_entry};
use serde_json::Value;

/// Why each invalid fixture must be rejected. The shared contract is only
/// the valid/invalid verdict; pinning the reason as well proves we reject
/// each fixture for the rule it exists to test, not incidentally. A new
/// fixture fails `every_fixture_has_an_expectation` until it is added here.
const EXPECTED: &[(&str, Option<C>)] = &[
    ("list_lifecycle_valid.json", None),
    ("add_missing_subject_agree.json", Some(C::Malformed)), // shape, rule 8
    ("bad_signature.json", Some(C::InvalidSignature)),
    ("bad_ts.json", Some(C::TimestampSkew)),                 // rule 7
    ("create_at_seq_nonzero.json", Some(C::HeadMismatch)),   // rule 1 before rule 2
    ("create_with_group.json", Some(C::Malformed)),          // shape, rule 8
    ("create_envelope_not_author.json", Some(C::Malformed)), // rule 8: K_0 for the author only
    ("create_envelope_owner_valid.json", None),
    ("integer_out_of_range.json", Some(C::Malformed)),
    ("non_owner_add.json", Some(C::NotOwner)),    // rule 3
    ("non_owner_remove.json", Some(C::NotOwner)), // rule 3
    ("noncanonical_case_variant_key.json", Some(C::Malformed)),
    ("noncanonical_duplicate_key.json", Some(C::Malformed)),
    ("noncanonical_key_order.json", Some(C::Malformed)),
    ("noncanonical_number_form.json", Some(C::Malformed)),
    ("noncanonical_unknown_field.json", Some(C::Malformed)),
    ("noncanonical_whitespace.json", Some(C::Malformed)),
    ("owner_removed.json", Some(C::OwnerStays)),                 // rule 6
    ("remove_without_envelopes.json", Some(C::MissingEnvelope)), // rule 4
    ("wrong_prev.json", Some(C::HeadMismatch)),                  // rule 1
    // Link invites; reasons as pinned in Go's TestLinkInviteFixturesFailForTheRightReason.
    ("link_invite_lifecycle_valid.json", None),
    ("entry_for_other_group.json", Some(C::Malformed)), // rule 1: group
    ("invite_by_non_owner.json", Some(C::NotOwner)),    // rule 3
    ("invite_expiry_too_far.json", Some(C::InviteExpiry)), // rule 9
    ("invite_key_reused.json", Some(C::InviteReused)),  // rule 9
    ("join_after_uninvite.json", Some(C::InviteNotOpen)), // rules 10, 11
    ("join_after_epoch_change.json", Some(C::InviteNotOpen)), // rules 11, 12
    ("join_consumed_invite.json", Some(C::InviteNotOpen)), // rule 11
    ("join_expired.json", Some(C::InviteExpired)),      // rule 11
    ("join_bad_proof.json", Some(C::JoinProof)),        // rule 11
    ("join_proof_for_other_list.json", Some(C::JoinProof)), // rule 11
    ("join_author_not_subject.json", Some(C::Malformed)), // shape, rule 8
];

struct Fixture {
    valid: bool,
    entries: Vec<SignedEntry>,
}

fn load(file: &str) -> Fixture {
    let v: Value = serde_json::from_str(&std::fs::read_to_string(fixtures_dir().join(file)).unwrap()).unwrap();
    let entries = v["entries"]
        .as_array()
        .unwrap()
        .iter()
        .map(|e| SignedEntry {
            raw: std_base64(e["raw"].as_str().unwrap()),
            sig: std_base64(e["sig"].as_str().unwrap()),
        })
        .collect();
    Fixture { valid: v["valid"].as_bool().unwrap(), entries }
}

fn fixture_files() -> Vec<String> {
    let mut files: Vec<String> = std::fs::read_dir(fixtures_dir())
        .unwrap()
        .map(|e| e.unwrap().file_name().into_string().unwrap())
        .filter(|f| f.ends_with(".json"))
        .collect();
    files.sort();
    files
}

#[test]
fn every_fixture_has_an_expectation() {
    let mut expected: Vec<&str> = EXPECTED.iter().map(|(f, _)| *f).collect();
    expected.sort();
    assert_eq!(fixture_files(), expected);
}

#[test]
fn fixtures_get_gos_verdict_for_the_right_reason() {
    for (file, want) in EXPECTED {
        let f = load(file);
        assert!(!f.entries.is_empty(), "{file}");
        assert_eq!(f.valid, want.is_none(), "{file}: expectation disagrees with the file's `valid`");

        let mut state: Option<LogState> = None;
        let mut last_error = None;
        for (i, e) in f.entries.iter().enumerate() {
            let is_last = i == f.entries.len() - 1;
            let verdict = parse_entry(&e.raw, &e.sig).and_then(|entry| validate_entry(state.as_ref(), &entry));
            match verdict {
                Ok(next) => state = Some(next),
                Err(err) => {
                    assert!(is_last, "{file}: setup entry {i} must be valid: {err}");
                    last_error = Some(err.code);
                }
            }
        }
        assert_eq!(last_error, *want, "{file}");
    }
}

#[test]
fn apply_entries_accepts_the_valid_lifecycle_end_to_end() {
    let f = load("list_lifecycle_valid.json");
    let (state, entries) = apply_entries(None, &f.entries).unwrap();
    let state = state.unwrap();
    assert_eq!(entries.len(), 3);
    assert_eq!(state.epoch, 1);
    assert_eq!(state.members.len(), 1);
}
