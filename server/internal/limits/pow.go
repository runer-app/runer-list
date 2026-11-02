package limits

import "crypto/sha256"

// DefaultSignupPoWBits is the default leading-zero-bit difficulty for the
// signup proof-of-work (Self-hosting: Limits), about a second of CPU.
//
// Kept simple: one public difficulty per server (SIGNUP_POW_BITS), advertised
// in every challenge response, instead of an adaptive per-challenge one. A
// per-challenge difficulty would need the challenge response to reveal
// whether the account already exists (skip PoW) or not (require it), which
// is itself an enumeration oracle; a value that is the same for everyone
// reveals nothing. Raise it if signup abuse is observed.
const DefaultSignupPoWBits = 20

// VerifyProofOfWork checks that sha256(authPub || nonce || powNonce) has at
// least bits leading zero bits.
func VerifyProofOfWork(authPub, nonce, powNonce []byte, bits int) bool {
	h := sha256.New()
	h.Write(authPub)
	h.Write(nonce)
	h.Write(powNonce)
	sum := h.Sum(nil)
	return leadingZeroBits(sum) >= bits
}

func leadingZeroBits(h []byte) int {
	count := 0
	for _, b := range h {
		if b == 0 {
			count += 8
			continue
		}
		for i := 7; i >= 0; i-- {
			if b&(1<<uint(i)) != 0 {
				return count
			}
			count++
		}
	}
	return count
}
