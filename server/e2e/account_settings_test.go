package e2e_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"runer-api/internal/auth"

	"github.com/stretchr/testify/require"
)

// An account starts without settings, at settings_gen 0. PUT /account/settings
// replaces the sealed blob (29..16384 bytes) only against the current
// settings_gen, and GET /account returns both.
func TestAccountSettings(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()
	c := newClient(t, baseURL)
	enc := base64.RawURLEncoding.EncodeToString
	require.NoError(t, c.Signup(ctx, []byte("b")))

	acc, err := c.GetAccount(ctx)
	require.NoError(t, err)
	require.Nil(t, acc.SettingsSealed)
	require.Zero(t, acc.SettingsGen)

	for _, bad := range []string{"", "not base64!", enc(make([]byte, 28)), enc(make([]byte, 16<<10+1))} {
		_, err := c.SetSettings(ctx, bad, 0)
		requireAPIError(t, err, "bad_request")
	}

	first := enc(bytes.Repeat([]byte{1}, 29))
	gen, err := c.SetSettings(ctx, first, 0)
	require.NoError(t, err)
	require.Equal(t, 1, gen)

	// A second device that still holds gen 0 loses the race and changes nothing.
	_, err = c.SetSettings(ctx, enc(bytes.Repeat([]byte{9}, 29)), 0)
	requireAPIError(t, err, "conflict")
	acc, err = c.GetAccount(ctx)
	require.NoError(t, err)
	require.NotNil(t, acc.SettingsSealed)
	require.Equal(t, first, *acc.SettingsSealed)
	require.Equal(t, 1, acc.SettingsGen)

	second := enc(bytes.Repeat([]byte{2}, 16<<10))
	gen, err = c.SetSettings(ctx, second, 1)
	require.NoError(t, err)
	require.Equal(t, 2, gen)
	acc, err = c.GetAccount(ctx)
	require.NoError(t, err)
	require.Equal(t, second, *acc.SettingsSealed)
	require.Equal(t, 2, acc.SettingsGen)
}
