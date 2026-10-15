package grouplog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFixtures is the shared valid/invalid membership-log contract:
// every JSON file under
// protocol/log-fixtures is replayed against Validate, and the last entry's
// outcome must match the fixture's `valid` flag. A client's validator must
// pass this same suite.
func TestFixtures(t *testing.T) {
	dir := fixturesDir(t)
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, files, "no fixtures found in %s — run GENERATE_FIXTURES=1 go test ./internal/grouplog/...", dir)

	for _, de := range files {
		if !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		t.Run(de.Name(), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, de.Name()))
			require.NoError(t, err)

			var f fixture
			require.NoError(t, json.Unmarshal(data, &f))
			require.NotEmpty(t, f.Entries)

			var state State
			var lastErr error
			for i, fe := range f.Entries {
				isLast := i == len(f.Entries)-1

				entry, err := Parse(fe.Raw, fe.Sig)
				if err != nil {
					require.Truef(t, isLast, "entry %d must parse and verify", i)
					lastErr = err
					break
				}

				next, verr := Validate(state, entry)
				lastErr = verr
				if !isLast {
					require.NoErrorf(t, verr, "setup entry %d must be valid", i)
					state = next
				}
			}

			if f.Valid {
				require.NoError(t, lastErr)
			} else {
				require.Error(t, lastErr)
			}
		})
	}
}

// TestLinkInviteFixturesFailForTheRightReason pins why each link-invite
// fixture is invalid, so a fixture can't pass TestFixtures by failing on
// an unrelated rule.
func TestLinkInviteFixturesFailForTheRightReason(t *testing.T) {
	want := map[string]error{
		"join_after_uninvite":        ErrInviteNotOpen,
		"join_after_epoch_change":    ErrInviteNotOpen,
		"join_expired":               ErrInviteExpired,
		"join_consumed_invite":       ErrInviteNotOpen,
		"join_bad_proof":             ErrJoinProof,
		"join_proof_for_other_list":  ErrJoinProof,
		"join_author_not_subject":    ErrMalformed,
		"invite_by_non_owner":        ErrNotOwner,
		"invite_expiry_too_far":      ErrInviteExpiry,
		"invite_key_reused":          ErrInviteReused,
		"entry_for_other_group":      ErrMalformed,
		"create_envelope_not_author": ErrMalformed,
	}
	for name, wantErr := range want {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(fixturesDir(t), name+".json"))
			require.NoError(t, err)
			var f fixture
			require.NoError(t, json.Unmarshal(data, &f))

			var state State
			for i, fe := range f.Entries {
				entry, err := Parse(fe.Raw, fe.Sig)
				if err == nil {
					state, err = Validate(state, entry)
				}
				if i < len(f.Entries)-1 {
					require.NoErrorf(t, err, "setup entry %d", i)
					continue
				}
				require.ErrorIs(t, err, wantErr)
			}
		})
	}
}
