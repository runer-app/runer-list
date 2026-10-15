// Package testclient is a deliberately simple Go test client: it can sign challenges, build log entries
// and send frames, so server-side e2e scenarios don't need a real
// client. It is not a complete client: e.g. it treats the identity bundle
// as an opaque blob, since bundle encryption is entirely the client's job
// and irrelevant to what the server enforces.
package testclient

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
)

// Identity stands in for a client-core account: an auth key pair (normally
// derived from the 12-word Account Key) and an identity key pair (normally
// random, wrapped in the encrypted bundle). The server only ever sees the
// public halves plus an opaque "bundle" blob, so generating these directly
// instead of deriving them from a BIP-39 phrase changes nothing the server
// can observe.
type Identity struct {
	AuthPub  ed25519.PublicKey
	AuthPriv ed25519.PrivateKey

	SignPub  ed25519.PublicKey
	SignPriv ed25519.PrivateKey

	AgreePriv *ecdh.PrivateKey
	AgreePub  []byte // raw X25519 public key bytes, as sent on the wire
}

// NewIdentity generates a fresh identity, as a client would at signup.
func NewIdentity() (*Identity, error) {
	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate auth key: %w", err)
	}
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate identity sign key: %w", err)
	}
	agreePriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate identity agree key: %w", err)
	}

	return &Identity{
		AuthPub:   authPub,
		AuthPriv:  authPriv,
		SignPub:   signPub,
		SignPriv:  signPriv,
		AgreePriv: agreePriv,
		AgreePub:  agreePriv.PublicKey().Bytes(),
	}, nil
}

// Rekey replaces the auth key pair in place, as "change account key" would
// (E2EE §9.2). Identity keys are untouched — that is the point.
func (id *Identity) Rekey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate new auth key: %w", err)
	}
	return pub, priv, nil
}
