package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJoinPageHandsTheFragmentToTheApp(t *testing.T) {
	e := New(nil, Options{Origin: "tauri://localhost", PublicURL: "http://localhost:8080"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/join", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html"))
	require.Contains(t, rec.Body.String(), "runer://join?host=")
	// Only the page's own inline script and style run, by hash.
	csp := rec.Header().Get("Content-Security-Policy")
	require.Equal(t, joinCSP, csp)
	require.Contains(t, csp, "script-src '"+cspHash(joinScript)+"'")
	require.NotContains(t, csp, "unsafe-inline")
}
