// Package cryptovectors checks the shared crypto test vectors in
// protocol/vectors against an independent Go implementation (standard
// library only). The vectors are the source of truth for client
// implementations; this test proves the byte layouts written in the E2EE
// doc are exactly what the files hold, and is what accepts a new or
// changed vector. Values the server itself computes are also
// checked against the server's own code (auth messages here, the content
// header in internal/sync).
package cryptovectors

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/hpke"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"runer-api/internal/auth"
	"runer-api/internal/grouplog"
)

const prefix = "runer/v1/"

func load(t *testing.T, file string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "protocol", "vectors", file))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, v))
}

// hexBytes decodes a lowercase hex JSON string.
type hexBytes []byte

func (h *hexBytes) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	d, err := hex.DecodeString(s)
	*h = d
	return err
}

func kdf(t *testing.T, ikm []byte, label string, n int) []byte {
	t.Helper()
	k, err := hkdf.Key(sha256.New, ikm, nil, prefix+label, n)
	require.NoError(t, err)
	return k
}

func gcm(t *testing.T, key []byte) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	a, err := cipher.NewGCM(block)
	require.NoError(t, err)
	return a
}

// openBox opens nonce(12) ‖ ciphertext ‖ tag.
func openBox(t *testing.T, key, box, ad []byte) []byte {
	t.Helper()
	p, err := gcm(t, key).Open(nil, box[:12], box[12:], ad)
	require.NoError(t, err)
	return p
}

// labelled is label ‖ 0x00 ‖ fields.
func labelled(label string, fields ...[]byte) []byte {
	out := append([]byte(prefix+label), 0)
	for _, f := range fields {
		out = append(out, f...)
	}
	return out
}

func x25519Pub(t *testing.T, priv []byte) []byte {
	t.Helper()
	k, err := ecdh.X25519().NewPrivateKey(priv)
	require.NoError(t, err)
	return k.PublicKey().Bytes()
}

func TestAccount(t *testing.T) {
	var v struct {
		Cases []struct {
			Name       string
			AccountKey hexBytes `json:"account_key"`
			AuthSeed   hexBytes `json:"auth_seed"`
			AuthPub    hexBytes `json:"auth_pub"`
			WrapKey    hexBytes `json:"wrap_key"`
			ServerURL  string   `json:"server_url"`
			Nonce      hexBytes
			Messages   map[string]struct {
				Message hexBytes
				AuthSig hexBytes `json:"auth_sig"`
			}
		}
	}
	load(t, "account.json", &v)
	require.NotEmpty(t, v.Cases)
	build := map[string]func(string, []byte) []byte{
		"login": auth.LoginMessage, "rekey": auth.RekeyMessage, "identity": auth.IdentityMessage,
	}
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			require.Len(t, c.AccountKey, 16)
			require.Equal(t, []byte(c.AuthSeed), kdf(t, c.AccountKey, "auth", 32))
			require.Equal(t, []byte(c.WrapKey), kdf(t, c.AccountKey, "wrap", 32))
			pub := ed25519.NewKeyFromSeed(c.AuthSeed).Public().(ed25519.PublicKey)
			require.Equal(t, []byte(c.AuthPub), []byte(pub))
			require.Len(t, c.Messages, len(build))
			for kind, m := range c.Messages {
				require.Equal(t, []byte(m.Message), build[kind](c.ServerURL, c.Nonce), kind)
				require.True(t, ed25519.Verify(pub, m.Message, m.AuthSig), kind)
			}
		})
	}
}

func TestIdentityBundle(t *testing.T) {
	var v struct {
		Cases []struct {
			Name         string
			WrapKey      hexBytes `json:"wrap_key"`
			AuthPub      hexBytes `json:"auth_pub"`
			BundleGen    uint64   `json:"bundle_gen"`
			SignSeed     hexBytes `json:"sign_seed"`
			SignPub      hexBytes `json:"sign_pub"`
			AgreePrivate hexBytes `json:"agree_private"`
			AgreePub     hexBytes `json:"agree_pub"`
			Nonce        hexBytes
			Bundle       hexBytes
		}
	}
	load(t, "identity-bundle.json", &v)
	require.NotEmpty(t, v.Cases)
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			ad := binary.BigEndian.AppendUint64(bytes.Clone(c.AuthPub), c.BundleGen)
			require.Equal(t, []byte(c.Nonce), []byte(c.Bundle[:12]))
			plain := openBox(t, c.WrapKey, c.Bundle, ad)
			require.Equal(t, append(append([]byte{1}, c.SignSeed...), c.AgreePrivate...), plain)
			require.Equal(t, []byte(c.SignPub), []byte(ed25519.NewKeyFromSeed(c.SignSeed).Public().(ed25519.PublicKey)))
			require.Equal(t, []byte(c.AgreePub), x25519Pub(t, c.AgreePrivate))
		})
	}
}

func TestListKeys(t *testing.T) {
	var v struct {
		List   hexBytes
		Epochs []struct {
			Epoch       int
			ListKey     hexBytes `json:"list_key"`
			DocumentKey hexBytes `json:"document_key"`
			KeyChain    hexBytes `json:"key_chain"`
		}
		Envelopes []struct {
			Name                  string
			List                  hexBytes
			Epoch                 uint64
			RecipientSignPub      hexBytes `json:"recipient_sign_pub"`
			RecipientAgreePub     hexBytes `json:"recipient_agree_pub"`
			RecipientAgreePrivate hexBytes `json:"recipient_agree_private"`
			EphemeralPrivate      hexBytes `json:"ephemeral_private"`
			EphemeralPub          hexBytes `json:"ephemeral_pub"`
			Info                  hexBytes
			ListKey               hexBytes `json:"list_key"`
			HPKECiphertext        hexBytes `json:"hpke_ciphertext"`
		}
	}
	load(t, "list-keys.json", &v)
	require.NotEmpty(t, v.Epochs)
	require.NotEmpty(t, v.Envelopes)

	for i, e := range v.Epochs {
		t.Run(fmt.Sprintf("epoch %d", e.Epoch), func(t *testing.T) {
			require.Equal(t, []byte(e.DocumentKey), kdf(t, e.ListKey, "list", 32))
			if i == 0 {
				require.Empty(t, e.KeyChain)
				return
			}
			// K_chain(e) is used once, so the nonce is all zeros; no AD.
			require.Len(t, e.KeyChain, 48)
			prev, err := gcm(t, kdf(t, e.ListKey, "chain", 32)).Open(nil, make([]byte, 12), e.KeyChain, nil)
			require.NoError(t, err)
			require.Equal(t, []byte(v.Epochs[i-1].ListKey), prev)
		})
	}

	for _, e := range v.Envelopes {
		t.Run("envelope "+e.Name, func(t *testing.T) {
			info := labelled("env", e.List, binary.BigEndian.AppendUint64(nil, e.Epoch), e.RecipientSignPub)
			require.Equal(t, []byte(e.Info), info)
			require.Equal(t, []byte(e.RecipientAgreePub), x25519Pub(t, e.RecipientAgreePrivate))
			require.Equal(t, []byte(e.EphemeralPub), x25519Pub(t, e.EphemeralPrivate))
			require.Equal(t, []byte(e.EphemeralPub), []byte(e.HPKECiphertext[:32]), "hpke_ciphertext starts with enc")

			priv, err := ecdh.X25519().NewPrivateKey(e.RecipientAgreePrivate)
			require.NoError(t, err)
			k, err := hpke.NewDHKEMPrivateKey(priv)
			require.NoError(t, err)
			key, err := hpke.Open(k, hpke.HKDFSHA256(), hpke.AES256GCM(), info, e.HPKECiphertext)
			require.NoError(t, err)
			require.Equal(t, []byte(e.ListKey), key)
		})
	}
}

func TestContent(t *testing.T) {
	var v struct {
		Cases []struct {
			Name        string
			DocumentKey hexBytes `json:"document_key"`
			AuthorSeed  hexBytes `json:"author_seed"`
			HeaderText  string   `json:"header_text"`
			Header      hexBytes
			Body        hexBytes
			Nonce       hexBytes
			CT          hexBytes
			Sig         hexBytes
		}
	}
	load(t, "content.json", &v)
	require.NotEmpty(t, v.Cases)
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			require.Equal(t, c.HeaderText, string(c.Header))
			pub := ed25519.NewKeyFromSeed(c.AuthorSeed).Public().(ed25519.PublicKey)
			msg := append(append(bytes.Clone(c.Header), c.Nonce...), c.CT...)
			require.True(t, ed25519.Verify(pub, msg, c.Sig))
			body, err := gcm(t, c.DocumentKey).Open(nil, c.Nonce, c.CT, c.Header)
			require.NoError(t, err)
			require.Equal(t, []byte(c.Body), body)
		})
	}
}

func TestInvite(t *testing.T) {
	var v struct {
		Secret         hexBytes
		ID             hexBytes
		Enc            hexBytes
		Mac            hexBytes
		PackageText    string `json:"package_text"`
		Package        hexBytes
		DisplayName    string   `json:"display_name"`
		JoinName       hexBytes `json:"join_name"`
		JoinerSignPub  hexBytes `json:"joiner_sign_pub"`
		JoinerAgreePub hexBytes `json:"joiner_agree_pub"`
		InviteProof    hexBytes `json:"invite_proof"`
	}
	load(t, "invite.json", &v)
	require.Len(t, v.Secret, 32)
	require.Equal(t, []byte(v.ID), kdf(t, v.Secret, "invite-id", 16))
	require.Equal(t, []byte(v.Enc), kdf(t, v.Secret, "invite-enc", 32))
	require.Equal(t, []byte(v.Mac), kdf(t, v.Secret, "invite-mac", 32))

	require.Equal(t, v.PackageText, string(openBox(t, v.Enc, v.Package, labelled("invite-package", v.ID))))
	require.Equal(t, v.DisplayName, string(openBox(t, v.Enc, v.JoinName, labelled("invite-join", v.ID))))

	m := hmac.New(sha256.New, v.Mac)
	m.Write(v.ID)
	m.Write(v.JoinerSignPub)
	m.Write(v.JoinerAgreePub)
	require.Equal(t, []byte(v.InviteProof), m.Sum(nil))
}

// TestLinkInvite checks the link-invite constructions (E2EE §6.1): the
// invite key derived from s, the package that also carries the list key,
// and the join proof, against the same s and joiner as TestInvite.
func TestLinkInvite(t *testing.T) {
	var v struct {
		Secret         hexBytes
		ID             hexBytes
		Enc            hexBytes
		JoinerSignPub  hexBytes `json:"joiner_sign_pub"`
		JoinerAgreePub hexBytes `json:"joiner_agree_pub"`
		Link           struct {
			Labels struct {
				InviteSign string `json:"invite_sign"`
				Join       string
			}
			InviteSignSeed hexBytes `json:"invite_sign_seed"`
			InvitePub      hexBytes `json:"invite_pub"`
			Epoch          int64
			ListKey        hexBytes `json:"list_key"`
			PackageText    string   `json:"package_text"`
			PackageNonce   hexBytes `json:"package_nonce"`
			Package        hexBytes
			JoinMessage    hexBytes `json:"join_message"`
			JoinProof      hexBytes `json:"join_proof"`
		}
	}
	load(t, "invite.json", &v)
	l := v.Link
	require.Equal(t, "runer/v1/invite-sign", l.Labels.InviteSign)
	require.Equal(t, grouplog.JoinProofLabel, l.Labels.Join)

	require.Equal(t, []byte(l.InviteSignSeed), kdf(t, v.Secret, "invite-sign", 32))
	invitePub := ed25519.NewKeyFromSeed(l.InviteSignSeed).Public().(ed25519.PublicKey)
	require.Equal(t, []byte(l.InvitePub), []byte(invitePub))

	// The link package: same key and AD as the approve-first one, with
	// owner_name (left out when empty), epoch and list_key after name, in
	// canonical form.
	require.Equal(t, []byte(l.PackageNonce), []byte(l.Package[:12]))
	require.Equal(t, l.PackageText, string(openBox(t, v.Enc, l.Package, labelled("invite-package", v.ID))))
	var pkg struct {
		V          int    `json:"v"`
		List       string `json:"list"`
		LogHead    string `json:"log_head"`
		Owner      string `json:"owner"`
		OwnerAgree string `json:"owner_agree"`
		Name       string `json:"name"`
		OwnerName  string `json:"owner_name,omitempty"`
		Epoch      int64  `json:"epoch"`
		ListKey    string `json:"list_key"`
	}
	require.NoError(t, json.Unmarshal([]byte(l.PackageText), &pkg))
	canonical, err := grouplog.CanonicalJSON(pkg)
	require.NoError(t, err)
	require.Equal(t, l.PackageText, string(canonical), "package keys in E2EE §6 order, canonical encoding")
	require.Equal(t, l.Epoch, pkg.Epoch)
	require.Equal(t, base64.RawURLEncoding.EncodeToString(l.ListKey), pkg.ListKey)

	listID, err := hex.DecodeString(pkg.List)
	require.NoError(t, err)
	require.Equal(t, []byte(l.JoinMessage), grouplog.JoinProofMessage(listID, v.JoinerSignPub, v.JoinerAgreePub))
	require.Len(t, l.JoinProof, ed25519.SignatureSize)
	require.True(t, ed25519.Verify(invitePub, l.JoinMessage, l.JoinProof))
}

func TestSafetyCode(t *testing.T) {
	var v struct {
		Cases []struct {
			Name     string
			SignPub  hexBytes `json:"sign_pub"`
			AgreePub hexBytes `json:"agree_pub"`
			Code     string
		}
	}
	load(t, "safety-code.json", &v)
	require.NotEmpty(t, v.Cases)
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			h := sha256.Sum256(labelled("safety", c.SignPub, c.AgreePub))
			groups := make([]string, 6)
			for i := range groups {
				var n uint64
				for _, b := range h[5*i : 5*i+5] {
					n = n<<8 | uint64(b)
				}
				groups[i] = fmt.Sprintf("%05d", n%100000)
			}
			require.Equal(t, c.Code, strings.Join(groups, " "))
		})
	}
}

// TestEd25519EdgeCases pins which unusual signatures ed25519.Verify accepts
// (E2EE §10): a client must give the same verdicts, so a Go upgrade that
// changed one would split the two silently.
func TestEd25519EdgeCases(t *testing.T) {
	var v struct {
		Cases []struct {
			Name  string
			Pub   hexBytes
			Msg   hexBytes
			Sig   hexBytes
			Valid bool
		}
	}
	load(t, "ed25519-edge.json", &v)
	require.NotEmpty(t, v.Cases)
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			require.Len(t, c.Pub, ed25519.PublicKeySize)
			require.Equal(t, c.Valid, ed25519.Verify(ed25519.PublicKey(c.Pub), c.Msg, c.Sig))
		})
	}
}
