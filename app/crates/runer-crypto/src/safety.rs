//! Safety code: a 30-digit fingerprint of one identity (E2EE §6).

use crate::labels;
use crate::primitives::{KEY_LEN, Result, require_len, sha256};

/// `h = SHA-256("…/safety" ‖ 0x00 ‖ sign_pub ‖ agree_pub)`; each of the first
/// six 5-byte chunks of `h`, read as `uint40be mod 100000`, gives one group of
/// five digits. Returns the six groups.
///
/// One code per identity: in an invite only the joiner's keys need checking
/// (the owner's arrive in the package, authenticated by the link secret), so
/// the owner's approval dialog shows the joiner's code and the joiner reads
/// out their own.
pub fn safety_code(sign_pub: &[u8], agree_pub: &[u8]) -> Result<[String; 6]> {
    require_len("sign key", sign_pub, KEY_LEN)?;
    require_len("agree key", agree_pub, KEY_LEN)?;
    let h = sha256(&[&labels::labelled(labels::SAFETY)[..], sign_pub, agree_pub].concat());
    Ok(std::array::from_fn(|i| {
        let n = h[5 * i..5 * i + 5].iter().fold(0u64, |n, &b| n << 8 | b as u64);
        format!("{:05}", n % 100_000)
    }))
}
