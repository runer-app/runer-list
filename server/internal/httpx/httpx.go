// Package httpx holds the small set of request/response helpers shared by
// every handler package: JSON envelope, bind+validate, and base64url key
// decoding. The envelope is described under Conventions in
// protocol/docs/api-reference.md.
package httpx

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"

	"filippo.io/edwards25519"
	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v5"
)

var validate = validator.New()

// X25519KeySize is the length of a raw X25519 public key.
const X25519KeySize = 32

// JSONData writes the `{"data": ...}` success envelope.
func JSONData(c *echo.Context, status int, data any) error {
	return c.JSON(status, map[string]any{"data": data})
}

// BindAndValidate binds the request body into dst and runs struct
// validation, returning a 400 on either failure.
func BindAndValidate(c *echo.Context, dst any) error {
	if err := c.Bind(dst); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "malformed request body")
	}
	if err := validate.Struct(dst); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "validation failed: "+err.Error())
	}
	return nil
}

// DecodeKey base64url-decodes s and checks it is exactly wantLen bytes.
func DecodeKey(s string, wantLen int) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != wantLen {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid key length")
	}
	return b, nil
}

// DecodeEd25519Key is DecodeKey for an Ed25519 public key that is being
// registered or used to authenticate. It also rejects non-canonical
// encodings and small-order points: for a small-order key anyone can make a
// signature that ed25519.Verify accepts. Signature checks stay plain
// ed25519.Verify; this only keeps such keys from entering the system.
func DecodeEd25519Key(s string) ([]byte, error) {
	b, err := DecodeKey(s, ed25519.PublicKeySize)
	if err != nil {
		return nil, err
	}
	p, err := new(edwards25519.Point).SetBytes(b)
	if err != nil || !bytes.Equal(p.Bytes(), b) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid key")
	}
	if new(edwards25519.Point).MultByCofactor(p).Equal(edwards25519.NewIdentityPoint()) == 1 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid key")
	}
	return b, nil
}
