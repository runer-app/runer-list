package httpx

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestDecodeEd25519Key(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEd25519Key(base64.RawURLEncoding.EncodeToString(pub)); err != nil {
		t.Fatalf("random key rejected: %v", err)
	}

	bad := map[string]string{
		"identity (order 1)":        "0100000000000000000000000000000000000000000000000000000000000000",
		"y = -1 (order 2)":          "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
		"y = 0 (order 4)":           "0000000000000000000000000000000000000000000000000000000000000000",
		"y = 0, sign bit (order 4)": "0000000000000000000000000000000000000000000000000000000000000080",
		"order 8":                   "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05",
		"identity, non-canonical y": "eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	}
	for name, h := range bad {
		b, _ := hex.DecodeString(h)
		if _, err := DecodeEd25519Key(base64.RawURLEncoding.EncodeToString(b)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if _, err := DecodeEd25519Key(base64.RawURLEncoding.EncodeToString(pub[:31])); err == nil {
		t.Error("short key accepted")
	}
}
