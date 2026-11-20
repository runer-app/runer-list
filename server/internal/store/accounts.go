package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Account is the row shape for accounts. IdentityBundle is opaque ciphertext
// to the server; it never decrypts it (E2EE §3.3).
type Account struct {
	ID               string
	AuthPub          []byte
	IdentitySignPub  []byte
	IdentityAgreePub []byte
	IdentityBundle   []byte
	BundleGen        int
	CreatedAt        time.Time
	LastSeenAt       time.Time
	InboxID          *string // nil only for accounts that predate the Inbox
	SettingsSealed   []byte  // the account's settings, sealed by the client; opaque. nil until first set
	SettingsGen      int
}

// Accounts wraps account queries against the pool.
type Accounts struct {
	pool *pgxpool.Pool
}

func NewAccounts(pool *pgxpool.Pool) *Accounts {
	return &Accounts{pool: pool}
}

const accountColumns = `id, auth_pub, identity_sign_pub, identity_agree_pub, identity_bundle,
	bundle_gen, created_at, last_seen_at, inbox_id,
	settings_sealed, settings_gen`

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.AuthPub, &a.IdentitySignPub, &a.IdentityAgreePub, &a.IdentityBundle,
		&a.BundleGen, &a.CreatedAt, &a.LastSeenAt, &a.InboxID,
		&a.SettingsSealed, &a.SettingsGen)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("scan account: %w", err)
	}
	return a, nil
}

// Create inserts a new account (signup) and its Inbox, the list whose
// genesis is inbox, in one transaction. bundle_gen starts at 1.
func (a *Accounts) Create(ctx context.Context, authPub, identitySignPub, identityAgreePub, identityBundle []byte, inbox LogEntry) (Account, error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return Account{}, fmt.Errorf("begin create account: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	err = tx.QueryRow(ctx, `INSERT INTO accounts
		(auth_pub, identity_sign_pub, identity_agree_pub, identity_bundle)
		VALUES ($1, $2, $3, $4)
		RETURNING id`,
		authPub, identitySignPub, identityAgreePub, identityBundle).Scan(&id)
	if isUniqueViolation(err) {
		return Account{}, ErrConflict
	}
	if err != nil {
		return Account{}, fmt.Errorf("insert account: %w", err)
	}
	acc, err := setInbox(ctx, tx, id, identitySignPub, inbox)
	if err != nil {
		return Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, fmt.Errorf("commit create account: %w", err)
	}
	return acc, nil
}

// setInbox creates the list whose genesis is inbox, owned by accountID
// under identityKey, and makes it the account's Inbox.
func setInbox(ctx context.Context, tx pgx.Tx, accountID string, identityKey []byte, inbox LogEntry) (Account, error) {
	if _, err := insertGenesis(ctx, tx, inbox, accountID, identityKey); err != nil {
		return Account{}, err
	}
	return scanAccount(tx.QueryRow(ctx, `UPDATE accounts SET inbox_id = $1 WHERE id = $2 RETURNING `+accountColumns, inbox.GroupID, accountID))
}

func (a *Accounts) GetByAuthPub(ctx context.Context, authPub []byte) (Account, error) {
	row := a.pool.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE auth_pub = $1`, authPub)
	return scanAccount(row)
}

// GetByIdentitySignPub looks up the account owning an identity signing key,
// e.g. to resolve a membership log entry's author/subject to an account.
func (a *Accounts) GetByIdentitySignPub(ctx context.Context, identitySignPub []byte) (Account, error) {
	row := a.pool.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE identity_sign_pub = $1`, identitySignPub)
	return scanAccount(row)
}

func (a *Accounts) GetByID(ctx context.Context, id string) (Account, error) {
	row := a.pool.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = $1`, id)
	return scanAccount(row)
}

// SetSettings replaces the account's sealed settings and returns the new
// settings_gen. expectedGen must equal the account's current settings_gen,
// otherwise ErrConflict (another device saved first, or the caller's view
// is stale): the server can't merge what it can't read.
func (a *Accounts) SetSettings(ctx context.Context, id string, settingsSealed []byte, expectedGen int) (int, error) {
	var gen int
	err := a.pool.QueryRow(ctx, `UPDATE accounts
		SET settings_sealed = $1, settings_gen = settings_gen + 1
		WHERE id = $2 AND settings_gen = $3
		RETURNING settings_gen`, settingsSealed, id, expectedGen).Scan(&gen)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrConflict
	}
	if err != nil {
		return 0, fmt.Errorf("set account settings: %w", err)
	}
	return gen, nil
}

// TouchLastSeen refreshes last_seen_at at most once a minute.
func (a *Accounts) TouchLastSeen(ctx context.Context, id string) error {
	_, err := a.pool.Exec(ctx, `UPDATE accounts SET last_seen_at = now()
		WHERE id = $1 AND last_seen_at < now() - interval '1 minute'`, id)
	if err != nil {
		return fmt.Errorf("touch account last_seen: %w", err)
	}
	return nil
}

// Rekey swaps the auth key and identity bundle and revokes every session
// except keepSessionID, in one transaction (E2EE §9.2): no window where
// the key changed but old sessions survive. expectedGen must equal the
// account's current bundle_gen, otherwise ErrConflict (a concurrent rekey
// already ran, or the caller's view is stale).
func (a *Accounts) Rekey(ctx context.Context, id, keepSessionID string, newAuthPub, newBundle []byte, expectedGen int) (Account, error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return Account{}, fmt.Errorf("begin rekey: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `UPDATE accounts
		SET auth_pub = $1, identity_bundle = $2, bundle_gen = bundle_gen + 1
		WHERE id = $3 AND bundle_gen = $4
		RETURNING `+accountColumns,
		newAuthPub, newBundle, id, expectedGen)
	acc, err := scanAccount(row)
	if errors.Is(err, ErrNotFound) || isUniqueViolation(err) {
		return Account{}, ErrConflict
	}
	if err != nil {
		return Account{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions
		WHERE account_id = $1 AND id != $2`, id, keepSessionID); err != nil {
		return Account{}, fmt.Errorf("delete other sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, fmt.Errorf("commit rekey: %w", err)
	}
	return acc, nil
}

// DeletedList is a list that went with its owner's account, and the other
// accounts that were in it.
type DeletedList struct {
	ID      string
	HeadSeq int64
	Members []string
}

// ownedLists locks and returns every list the account owns.
const ownedLists = `SELECT g.id, g.head_seq FROM groups g
	JOIN memberships m ON m.group_id = g.id
	WHERE m.account_id = $1 AND m.role = 'owner'
	ORDER BY g.id
	FOR UPDATE OF g`

// Delete deletes an account at once, in one transaction: every list it
// owns (its Inbox and the lists it shares, for all their members), its
// sessions, its join requests (approved ones too), its pending rekey
// challenges and the account row with its keys, bundle and settings. No
// history row points at an account, so nothing keeps it. It returns the
// deleted lists that had other members.
//
// ErrAccountHasLists, with nothing changed, while the account is a member of
// a list it doesn't own: only a signed `leave` takes a member out of a log
// (E2EE §8.4), and the server can't sign one.
func (a *Accounts) Delete(ctx context.Context, id string) ([]DeletedList, error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin delete account: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	collect := func() ([]DeletedList, error) {
		rows, err := tx.Query(ctx, ownedLists, id)
		if err != nil {
			return nil, fmt.Errorf("lock owned lists: %w", err)
		}
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (DeletedList, error) {
			var l DeletedList
			err := row.Scan(&l.ID, &l.HeadSeq)
			return l, err
		})
	}
	// Lists before the account: the same lock order as an append, which
	// locks its list and then the accounts it names.
	if _, err := collect(); err != nil {
		return nil, err
	}
	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM accounts WHERE id = $1 FOR UPDATE`, id).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock account: %w", err)
	}
	// Again under the account's lock, which a new list, add or join waits
	// for (requireAccountKey): a list created just before is in this set,
	// and no membership can be added from here on.
	owned, err := collect()
	if err != nil {
		return nil, err
	}
	var inLists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM memberships WHERE account_id = $1 AND role != 'owner')`, id).Scan(&inLists); err != nil {
		return nil, fmt.Errorf("check account lists: %w", err)
	}
	if inLists {
		return nil, ErrAccountHasLists
	}

	var shared []DeletedList
	for _, l := range owned {
		rows, err := tx.Query(ctx, `SELECT account_id::text FROM memberships WHERE group_id = $1 AND account_id != $2`, l.ID, id)
		if err != nil {
			return nil, fmt.Errorf("list members of deleted list: %w", err)
		}
		if l.Members, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return nil, fmt.Errorf("list members of deleted list: %w", err)
		}
		if len(l.Members) > 0 {
			shared = append(shared, l)
		}
		if err := deleteList(ctx, tx, l.ID); err != nil {
			return nil, err
		}
	}
	for _, stmt := range []string{
		`DELETE FROM sessions WHERE account_id = $1`,
		`DELETE FROM join_requests WHERE account_id = $1`,
		`DELETE FROM challenges WHERE kind = 'rekey' AND subject = $1::text`,
		`DELETE FROM accounts WHERE id = $1`,
	} {
		if _, err := tx.Exec(ctx, stmt, id); err != nil {
			return nil, fmt.Errorf("delete account: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit delete account: %w", err)
	}
	return shared, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
