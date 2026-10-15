package auth

import "encoding/binary"

// Canonical signed-message construction, shared by the server (to verify)
// and the Go test client (to sign), so the two can never drift apart.
// See E2EE §3.4 (login) and §9.2 (rekey).

// LoginMessage is what the client signs with the auth key to prove it holds
// the private half of auth_pub. It binds the canonical API URL the client
// connected to, so a signature for one server can't be relayed to another
// that shares the same credential.
func LoginMessage(serverURL string, nonce []byte) []byte {
	return buildMessage("runer/v1/login", serverURL, nonce)
}

// RekeyMessage authenticates a rekey request with the OLD auth key, reusing
// the same challenge/nonce mechanism as login (E2EE §9.2: the request is signed
// with the old auth key).
func RekeyMessage(serverURL string, nonce []byte) []byte {
	return buildMessage("runer/v1/rekey", serverURL, nonce)
}

// IdentityMessage is what the client signs with a NEW identity signing key
// at signup, to prove it holds the private half: an account can only
// register an identity key it owns. nonce is the login challenge.
func IdentityMessage(serverURL string, nonce []byte) []byte {
	return buildMessage("runer/v1/identity", serverURL, nonce)
}

// buildMessage is label || 0x00 || uint16be(len(serverURL)) || serverURL ||
// nonce: the label is domain separation, the length prefix makes the URL /
// nonce boundary unambiguous. The nonce is always 32 bytes.
func buildMessage(label, serverURL string, nonce []byte) []byte {
	msg := make([]byte, 0, len(label)+3+len(serverURL)+len(nonce))
	msg = append(msg, label...)
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(serverURL)))
	msg = append(msg, serverURL...)
	msg = append(msg, nonce...)
	return msg
}
