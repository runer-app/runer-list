package e2e_test

import (
	"context"
	"net/http"
	"testing"

	"runer-api/internal/auth"

	"github.com/stretchr/testify/require"
)

// The server holds no personal account info: GET /account returns no name
// and no email.
func TestAccountHasNoNameOrEmail(t *testing.T) {
	baseURL, _ := newTestServer(t, auth.DefaultChallengeTTL)
	ctx := context.Background()

	c := newClient(t, baseURL)
	require.NoError(t, c.Signup(ctx, []byte("b")))

	var acc map[string]any
	require.NoError(t, c.Do(ctx, http.MethodGet, "/v1/account", nil, &acc))
	require.Equal(t, c.AccountID, acc["id"])
	require.NotContains(t, acc, "name")
	require.NotContains(t, acc, "email")
}
