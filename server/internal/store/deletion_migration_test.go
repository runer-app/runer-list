package store_test

import (
	"context"
	"testing"

	"runer-api/internal/store"
	"runer-api/internal/store/storetest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// A database from before immediate deletion holds lists and accounts that
// were soft-deleted and waiting for the retention window, among them an
// account the old purge could never remove because history it authored
// still pointed at it. The migration removes all of them and leaves the
// live ones alone.
func TestImmediateDeletionMigrationRemovesSoftDeletedRows(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, storetest.StartPostgres(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, store.MigrateUpTo(ctx, pool, "0006_invite_join_budget.sql"))

	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		require.NoError(t, err)
	}
	logEntry := func(list string) {
		exec(`INSERT INTO group_log (group_id, seq, entry, sig, hash, ts) VALUES ($1, 0, 'e', 's', 'h', now())`, list)
	}
	session := func(account []byte) {
		exec(`INSERT INTO sessions (account_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 day')`, account, randBytes(t, 32))
	}

	// Alice and her list stay. Dave, deleted, wrote an update in it,
	// created an invite on it (an older server allowed it) and joined
	// through it, then left: the old purge was blocked by both.
	alice, aliceKey := seedAccount(t, ctx, pool)
	kept := seedList(t, ctx, pool, alice, aliceKey)
	logEntry(kept)
	session(alice)
	dave, _ := seedAccount(t, ctx, pool)
	exec(`UPDATE accounts SET deleted_at = now() - interval '40 days' WHERE id = $1`, dave)
	exec(`INSERT INTO list_updates (list_id, seq, epoch, author_account_id, msg) VALUES ($1, 1, 0, $2, 'by-dave')`, kept, dave)
	exec(`UPDATE groups SET update_seq = 1 WHERE id = $1`, kept)
	exec(`INSERT INTO invites (id, group_id, created_by, package, expires_at) VALUES ('dave-inv', $1, $2, 'pkg', now())`, kept, dave)
	exec(`INSERT INTO join_requests (invite_id, account_id, enc_display_name, mac, approved_at) VALUES ('dave-inv', $1, 'n', 'm', now())`, dave)
	exec(`INSERT INTO invite_join_budget (invite_id, tokens, updated_at) VALUES ('dave-inv', 1, now())`)
	session(dave)
	exec(`INSERT INTO challenges (kind, subject, nonce, expires_at) VALUES ('rekey', $1, 'n', now())`, uuidString(dave))

	// Erin, deleted a day ago, with her Inbox and a list shared with
	// Alice, deleted with her and holding everything a list can hold.
	erin, erinKey := seedAccount(t, ctx, pool)
	exec(`UPDATE accounts SET deleted_at = now() - interval '1 day' WHERE id = $1`, erin)
	inbox := seedList(t, ctx, pool, erin, erinKey)
	exec(`UPDATE accounts SET inbox_id = $1 WHERE id = $2`, inbox, erin)
	shared := seedList(t, ctx, pool, erin, erinKey)
	for _, l := range []string{inbox, shared} {
		exec(`UPDATE groups SET deleted_at = now() - interval '1 day' WHERE id = $1`, l)
		logEntry(l)
		exec(`INSERT INTO list_updates (list_id, seq, epoch, author_account_id, msg) VALUES ($1, 1, 0, $2, 'by-erin')`, l, erin)
	}
	exec(`INSERT INTO memberships (group_id, account_id, identity_sign_pub, role) VALUES ($1, $2, $3, 'member')`, shared, alice, aliceKey)
	exec(`INSERT INTO list_updates (list_id, seq, epoch, author_account_id, msg) VALUES ($1, 2, 0, $2, 'by-alice')`, shared, alice)
	exec(`INSERT INTO list_snapshots (list_id, epoch, covers_seq, msg) VALUES ($1, 0, 2, 'snap')`, shared)
	exec(`INSERT INTO group_invites (group_id, invite_pub, expires_at, open) VALUES ($1, 'pub', now(), true)`, shared)
	exec(`INSERT INTO invites (id, group_id, created_by, package, expires_at, invite_pub) VALUES ('erin-inv', $1, $2, 'pkg', now(), 'pub')`, shared, erin)
	exec(`INSERT INTO join_requests (invite_id, account_id, enc_display_name, mac) VALUES ('erin-inv', $1, 'n', 'm')`, alice)
	exec(`INSERT INTO invite_join_budget (invite_id, tokens, updated_at) VALUES ('erin-inv', 1, now())`)
	session(erin)

	require.NoError(t, store.Migrate(ctx, pool))

	ids := func(sql string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, sql)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			require.NoError(t, rows.Scan(&s))
			out = append(out, s)
		}
		require.NoError(t, rows.Err())
		return out
	}
	require.Equal(t, []string{uuidString(alice)}, ids(`SELECT id::text FROM accounts`))
	require.Equal(t, []string{kept}, ids(`SELECT id FROM groups`))
	require.Equal(t, []string{"by-dave"}, ids(`SELECT convert_from(msg, 'UTF8') FROM list_updates`),
		"history Dave wrote stays in the list that still exists")
	require.Equal(t, []string{kept}, ids(`SELECT group_id FROM group_log`))
	require.Equal(t, []string{kept}, ids(`SELECT group_id FROM memberships`))
	require.Equal(t, []string{"dave-inv"}, ids(`SELECT id FROM invites`))
	require.Equal(t, []string{"dave-inv"}, ids(`SELECT invite_id FROM invite_join_budget`))
	require.Equal(t, []string{uuidString(alice)}, ids(`SELECT account_id::text FROM sessions`))
	for _, table := range []string{"join_requests", "list_snapshots", "group_invites", "challenges"} {
		require.Empty(t, ids(`SELECT 'row' FROM `+table), table)
	}
	require.Empty(t, ids(`SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE (table_name IN ('groups', 'accounts') AND column_name = 'deleted_at')
		   OR (table_name = 'list_updates' AND column_name = 'author_account_id')
		   OR (table_name = 'invites' AND column_name = 'created_by')`))

	// The kept list takes writes as before.
	groups, updates := store.NewGroups(pool), store.NewUpdates(pool)
	var seq int64
	require.NoError(t, groups.AuthorizedWrite(ctx, kept, uuidString(alice), 0, func(tx pgx.Tx) error {
		up, err := updates.Append(ctx, tx, kept, 0, []byte(`{}`))
		seq = up.Seq
		return err
	}))
	require.EqualValues(t, 2, seq)
}
