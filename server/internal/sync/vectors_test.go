package sync

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestContentHeaderVectors checks that the content headers in the shared
// crypto vectors (protocol/vectors/content.json, E2EE §7) pass the server's
// own parse and canonical-form check, with author and covers_seq as the
// client signed them.
func TestContentHeaderVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "protocol", "vectors", "content.json"))
	require.NoError(t, err)
	var v struct {
		Cases []struct {
			Name       string `json:"name"`
			AuthorSeed string `json:"author_seed"`
			Header     string `json:"header"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &v))
	require.NotEmpty(t, v.Cases)

	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			raw, err := hex.DecodeString(c.Header)
			require.NoError(t, err)
			seed, err := hex.DecodeString(c.AuthorSeed)
			require.NoError(t, err)

			var hdr updateHeader
			require.NoError(t, json.Unmarshal(raw, &hdr))
			require.NoError(t, requireCanonicalHeader(raw, hdr))
			require.Equal(t, c.Name, hdr.Type)
			require.Equal(t, c.Name == "snapshot", hdr.CoversSeq != nil)
			pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
			require.Equal(t, base64.RawURLEncoding.EncodeToString(pub), hdr.Author)
		})
	}
}
