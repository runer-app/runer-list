package grouplog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzParseAndValidate exercises Parse and Validate against adversarial
// byte input (one minute in CI). The property under test is robustness, not semantic
// correctness — fuzzing can't forge valid Ed25519 signatures, so almost
// every input is rejected by Parse; what must never happen, for any input,
// is a panic.
func FuzzParseAndValidate(f *testing.F) {
	for _, seed := range fuzzSeeds(f) {
		f.Add(seed.raw, seed.sig)
	}

	f.Fuzz(func(t *testing.T, raw, sig []byte) {
		entry, err := Parse(raw, sig)
		if err != nil {
			return
		}

		// Whatever parsed must validate without panicking, against both a
		// fresh group and an existing one shaped to at least let the
		// author's own head/prev line up.
		_, _ = Validate(State{}, entry)
		_, _ = Validate(State{
			Exists: true, ListID: entry.Group,
			HeadSeq: entry.Seq - 1, HeadHash: entry.Prev,
			Members: map[string]Role{b64(entry.Author): RoleOwner},
		}, entry)
		// And one where the entry's invite key is open and the author is
		// not yet a member, so join/uninvite reach their proof checks.
		_, _ = Validate(State{
			Exists: true, ListID: entry.Group,
			HeadSeq: entry.Seq - 1, HeadHash: entry.Prev,
			Members: map[string]Role{},
			Invites: map[string]InviteState{b64(entry.Invite): {Expires: entry.Ts, Open: true}},
		}, entry)
	})
}

type fuzzSeed struct{ raw, sig []byte }

// fuzzSeeds loads every entry from every committed fixture as fuzzer seed
// material — real, signature-valid JSON for the mutator to work from.
func fuzzSeeds(f *testing.F) []fuzzSeed {
	f.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "protocol", "log-fixtures"))
	if err != nil {
		f.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		f.Fatal(err)
	}

	var seeds []fuzzSeed
	for _, de := range files {
		if !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			f.Fatal(err)
		}
		var fx fixture
		if err := json.Unmarshal(data, &fx); err != nil {
			f.Fatal(err)
		}
		for _, e := range fx.Entries {
			seeds = append(seeds, fuzzSeed{raw: e.Raw, sig: e.Sig})
		}
	}
	return seeds
}
