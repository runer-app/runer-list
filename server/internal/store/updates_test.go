package store_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"sort"
	gosync "sync"
	"sync/atomic"
	"testing"

	"runer-api/internal/store"
	"runer-api/internal/store/storetest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// seedList inserts the minimum rows Groups.AuthorizedWrite checks: a live
// list at epoch 0 and accountID as its owner.
func seedList(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID, signPub []byte) string {
	t.Helper()
	id := fmt.Sprintf("%x", randBytes(t, 32))
	_, err := pool.Exec(ctx, `INSERT INTO groups (id, head_seq, head_hash, epoch) VALUES ($1, 0, $2, 0)`, id, randBytes(t, 32))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO memberships (group_id, account_id, identity_sign_pub, role) VALUES ($1, $2, $3, 'owner')`,
		id, accountID, signPub)
	require.NoError(t, err)
	return id
}

func seedAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (id, signPub []byte) {
	t.Helper()
	signPub = randBytes(t, 32)
	var uuid [16]byte
	err := pool.QueryRow(ctx, `INSERT INTO accounts (auth_pub, identity_sign_pub, identity_agree_pub, identity_bundle)
		VALUES ($1, $2, $3, $4) RETURNING id`, randBytes(t, 32), signPub, randBytes(t, 32), []byte("bundle")).Scan(&uuid)
	require.NoError(t, err)
	return uuid[:], signPub
}

func randBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func uuidString(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Several server instances (one pool each) write to the same lists at once
// while readers page through them the way catch-up does (ListSince from the
// highest seq seen). Nothing serializes the writers but the database. Every
// update must be seen by the reader, and each list's seqs must be 1..N.
func TestConcurrentWritersAcrossInstancesNeverHideAnUpdate(t *testing.T) {
	ctx := context.Background()
	url := storetest.StartPostgres(t)

	const instances = 3
	pools := make([]*pgxpool.Pool, instances)
	for i := range pools {
		p, err := store.Open(ctx, url)
		require.NoError(t, err)
		t.Cleanup(p.Close)
		pools[i] = p
	}

	acct, signPub := seedAccount(t, ctx, pools[0])
	accountID := uuidString(acct)
	lists := []string{seedList(t, ctx, pools[0], acct, signPub), seedList(t, ctx, pools[0], acct, signPub)}

	const writersPerList, writesPerWriter = 6, 40
	var written gosync.Map // list -> *[]int64
	for _, l := range lists {
		written.Store(l, &[]int64{})
	}
	var mu gosync.Mutex
	var writersDone atomic.Bool
	var wg gosync.WaitGroup
	for li, l := range lists {
		for w := 0; w < writersPerList; w++ {
			pool := pools[(li*writersPerList+w)%instances]
			wg.Add(1)
			go func() {
				defer wg.Done()
				groups, updates := store.NewGroups(pool), store.NewUpdates(pool)
				for i := 0; i < writesPerWriter; i++ {
					var up store.Update
					err := groups.AuthorizedWrite(ctx, l, accountID, 0, func(tx pgx.Tx) error {
						var err error
						up, err = updates.Append(ctx, tx, l, 0, accountID, []byte(`{}`))
						return err
					})
					if err != nil {
						t.Error(err)
						return
					}
					v, _ := written.Load(l)
					mu.Lock()
					*v.(*[]int64) = append(*v.(*[]int64), up.Seq)
					mu.Unlock()
				}
			}()
		}
	}

	// One reader per list, on its own instance, doing catch-up in a loop.
	seen := make([][]int64, len(lists))
	var rg gosync.WaitGroup
	for li, l := range lists {
		rg.Add(1)
		go func() {
			defer rg.Done()
			updates := store.NewUpdates(pools[(li+1)%instances])
			var head int64
			for {
				done := writersDone.Load()
				batch, err := updates.ListSince(ctx, l, head, 1000)
				if err != nil {
					t.Error(err)
					return
				}
				for _, u := range batch {
					seen[li] = append(seen[li], u.Seq)
					head = u.Seq
				}
				if done && len(batch) == 0 {
					return
				}
			}
		}()
	}

	wg.Wait()
	writersDone.Store(true)
	rg.Wait()

	for li, l := range lists {
		v, _ := written.Load(l)
		got := append([]int64(nil), *v.(*[]int64)...)
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		require.Len(t, got, writersPerList*writesPerWriter)
		require.Equal(t, got, seen[li], "catch-up from the highest seq seen must not skip an update")
		for i, s := range got {
			require.EqualValues(t, i+1, s, "seqs of a list are dense, starting at 1")
		}
	}
}

// A database written by a server from before per-list seqs (one global
// sequence) keeps its seqs, and each list counts on from its own highest.
func TestPerListSeqMigrationContinuesFromExistingSeqs(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, storetest.StartPostgres(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, store.MigrateUpTo(ctx, pool, "0003_delete_ended_sessions.sql"))

	acct, signPub := seedAccount(t, ctx, pool)
	a, b, empty := seedList(t, ctx, pool, acct, signPub), seedList(t, ctx, pool, acct, signPub), seedList(t, ctx, pool, acct, signPub)
	// Interleaved on the old global sequence: a gets 1, 3, 4; b gets 2.
	for _, l := range []string{a, b, a, a} {
		_, err := pool.Exec(ctx, `INSERT INTO list_updates (list_id, epoch, author_account_id, msg) VALUES ($1, 0, $2, '{}')`, l, acct)
		require.NoError(t, err)
	}

	require.NoError(t, store.Migrate(ctx, pool))

	groups, updates := store.NewGroups(pool), store.NewUpdates(pool)
	accountID := uuidString(acct)
	appendOne := func(l string) int64 {
		var up store.Update
		require.NoError(t, groups.AuthorizedWrite(ctx, l, accountID, 0, func(tx pgx.Tx) error {
			var err error
			up, err = updates.Append(ctx, tx, l, 0, accountID, []byte(`{}`))
			return err
		}))
		return up.Seq
	}
	require.EqualValues(t, 5, appendOne(a))
	require.EqualValues(t, 3, appendOne(b))
	require.EqualValues(t, 1, appendOne(empty))

	// A server from before 0004, still running during a rolling deploy,
	// inserts without a seq: the list's counter supplies it.
	_, err = pool.Exec(ctx, `INSERT INTO list_updates (list_id, epoch, author_account_id, msg) VALUES ($1, 0, $2, '{}')`, a, acct)
	require.NoError(t, err)
	require.EqualValues(t, 7, appendOne(a), "and the counter moved on")

	got, err := updates.ListSince(ctx, a, 0, 100)
	require.NoError(t, err)
	var seqs []int64
	for _, u := range got {
		seqs = append(seqs, u.Seq)
	}
	require.Equal(t, []int64{1, 3, 4, 5, 6, 7}, seqs)
}
