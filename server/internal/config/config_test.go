package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func setRequired(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("ORIGIN", "tauri://localhost")
	t.Setenv("PUBLIC_URL", "https://api.example.com")
}

func TestLimitsDefaultToZeroWhenUnset(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Zero(t, cfg.SignupsPerIPPerHour)
	require.Zero(t, cfg.SignupPoWBits)
}

func TestLimitsParseAndBound(t *testing.T) {
	setRequired(t)
	t.Setenv("SIGNUPS_PER_IP_PER_HOUR", "500")
	t.Setenv("SIGNUP_POW_BITS", "22")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 500, cfg.SignupsPerIPPerHour)
	require.Equal(t, 22, cfg.SignupPoWBits)

	for _, bad := range []struct{ name, value string }{
		{"SIGNUP_POW_BITS", "4"},  // trivial work
		{"SIGNUP_POW_BITS", "40"}, // minutes on a phone
		{"SIGNUPS_PER_IP_PER_HOUR", "0"},
		{"CHALLENGES_PER_IP_PER_MINUTE", "lots"},
	} {
		t.Run(bad.name+"="+bad.value, func(t *testing.T) {
			setRequired(t)
			t.Setenv(bad.name, bad.value)
			_, err := Load()
			require.ErrorContains(t, err, bad.name)
		})
	}
}
