package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Group is a list: a membership log plus one encrypted document.
type Group struct {
	ID        string
	HeadSeq   int64
	HeadHash  []byte
	Epoch     int64
	CreatedAt time.Time
}

type LogEntry struct {
	GroupID string
	Seq     int64
	Entry   []byte
	Sig     []byte
	Hash    []byte
	Ts      time.Time
}

// MemberView is a group membership row with the identity key the log
// admitted, which is what membership-log validation actually keys on.
type MemberView struct {
	AccountID       string
	IdentitySignPub []byte
	Role            string
}

// MembershipDiff is the change to memberships a log entry causes, applied
// atomically alongside the entry append.
type MembershipDiff struct {
	Add    []MemberView
	Remove []string // account ids
	Invite InviteChange
}

// LogInvite is one link invite as the log left it (E2EE §6).
type LogInvite struct {
	InvitePub []byte
	ExpiresAt time.Time
	Open      bool
}

// InviteChange is what one entry does to the list's link invites.
type InviteChange struct {
	Open    *LogInvite // an `invite` entry
	Close   []byte     // an `uninvite` or `join`: this invite key
	Joined  bool       // Close came from a join: its package is consumed, not revoked
	VoidAll bool       // the epoch changed: every open invite closes
}

type Groups struct {
	pool *pgxpool.Pool
}

func NewGroups(pool *pgxpool.Pool) *Groups {
	return &Groups{pool: pool}
}

const groupColumns = `id, head_seq, head_hash, epoch, created_at`

// accessibleMembers is the one access predicate, shared by
// Access, ListForAccount and the notification audience: member m belongs
// to list g under the identity key its account holds. Callers add a
// WHERE clause.
const accessibleMembers = `groups g
	JOIN memberships m ON m.group_id = g.id
	JOIN accounts a ON a.id = m.account_id AND a.identity_sign_pub = m.identity_sign_pub`

const groupColumnsG = `g.id, g.head_seq, g.head_hash, g.epoch, g.created_at`

func scanGroup(row pgx.Row) (Group, error) {
	var g Group
	err := row.Scan(&g.ID, &g.HeadSeq, &g.HeadHash, &g.Epoch, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	if err != nil {
		return Group{}, fmt.Errorf("scan group: %w", err)
	}
	return g, nil
}

// CreateGenesis creates a new list, its genesis log entry, and the
// creator's owner membership, atomically. entry.GroupID is the caller-computed
// SHA-256 of the genesis entry bytes (E2EE §4.1: "group id = SHA-256(genesis
// entry bytes)"). creatorKey is the identity key that signed the genesis.
func (g *Groups) CreateGenesis(ctx context.Context, entry LogEntry, creatorAccountID string, creatorKey []byte) (Group, error) {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return Group{}, fmt.Errorf("begin create genesis: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := insertGenesis(ctx, tx, entry, creatorAccountID, creatorKey)
	if err != nil {
		return Group{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Group{}, fmt.Errorf("commit create genesis: %w", err)
	}
	return created, nil
}

// insertGenesis is CreateGenesis inside the caller's transaction.
func insertGenesis(ctx context.Context, tx pgx.Tx, entry LogEntry, creatorAccountID string, creatorKey []byte) (Group, error) {
	created, err := scanGroup(tx.QueryRow(ctx, `INSERT INTO groups (id, head_seq, head_hash, epoch)
		VALUES ($1, 0, $2, 0)
		RETURNING `+groupColumns,
		entry.GroupID, entry.Hash))
	if isUniqueViolation(err) {
		return Group{}, ErrConflict
	}
	if err != nil {
		return Group{}, err
	}
	if err := requireAccountKey(ctx, tx, creatorAccountID, creatorKey); err != nil {
		return Group{}, err
	}
	if err := insertLogEntry(ctx, tx, entry); err != nil {
		return Group{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO memberships (group_id, account_id, identity_sign_pub, role) VALUES ($1, $2, $3, 'owner')`,
		entry.GroupID, creatorAccountID, creatorKey); err != nil {
		return Group{}, fmt.Errorf("insert genesis membership: %w", err)
	}
	return created, nil
}

// IsInbox reports whether the list is some account's current Inbox, which
// can't be deleted or shared. A list never becomes an Inbox after it is
// created, so this needs no lock.
func (g *Groups) IsInbox(ctx context.Context, id string) (bool, error) {
	return isInbox(ctx, g.pool, id)
}

func isInbox(ctx context.Context, q querier, id string) (bool, error) {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE inbox_id = $1)`, id).Scan(&ok); err != nil {
		return false, fmt.Errorf("check inbox: %w", err)
	}
	return ok, nil
}

func (g *Groups) Get(ctx context.Context, id string) (Group, error) {
	row := g.pool.QueryRow(ctx, `SELECT `+groupColumns+` FROM groups WHERE id = $1`, id)
	return scanGroup(row)
}

// ListForAccount returns every list the account can access right now: the
// same predicate as Access.
func (g *Groups) ListForAccount(ctx context.Context, accountID string) ([]Group, error) {
	rows, err := g.pool.Query(ctx, `SELECT `+groupColumnsG+` FROM `+accessibleMembers+`
		WHERE m.account_id = $1
		ORDER BY g.created_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list groups for account: %w", err)
	}
	defer rows.Close()

	var out []Group
	for rows.Next() {
		grp, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, grp)
	}
	return out, rows.Err()
}

// AppendGuard carries what AppendEntry must re-check inside its
// transaction, because it was read before the transaction began.
type AppendGuard struct {
	// InviteMAC: for an `add` carrying invite_proof, the invite
	// whose pending join request from SubjectAccountID carried this MAC is
	// consumed and the request approved, in the same transaction. No
	// matching usable invite: ErrInviteNotUsable, nothing is appended.
	InviteMAC        []byte
	SubjectAccountID string
	// Accounts the entry depends on (its author and an `add` subject) must
	// still hold the identity key it names. Their rows are locked, which
	// orders the append against Accounts.Delete.
	Accounts []MemberView
}

// ErrInviteNotUsable: invite_proof matches no pending join request by the
// added account on an unexpired, unrevoked, unconsumed invite.
var ErrInviteNotUsable = errors.New("invite not usable")

// AppendEntry appends a log entry and applies its membership diff,
// atomically, gated by a compare-and-swap on the current head (E2EE §4.3:
// "Appending is compare-and-swap on prev"). Returns ErrConflict if the
// list's head has moved since the caller last read it.
func (g *Groups) AppendEntry(ctx context.Context, entry LogEntry, newHeadHash []byte, newEpoch int64, diff MembershipDiff, guard AppendGuard) (Group, error) {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return Group{}, fmt.Errorf("begin append entry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `UPDATE groups SET head_seq = $1, head_hash = $2, epoch = $3
		WHERE id = $4 AND head_seq = $5
		RETURNING `+groupColumns,
		entry.Seq, newHeadHash, newEpoch, entry.GroupID, entry.Seq-1)
	updated, err := scanGroup(row)
	if errors.Is(err, ErrNotFound) {
		return Group{}, ErrConflict
	}
	if err != nil {
		return Group{}, err
	}

	if len(guard.InviteMAC) > 0 {
		if err := consumeInvite(ctx, tx, entry.GroupID, guard.InviteMAC, guard.SubjectAccountID); err != nil {
			return Group{}, err
		}
	}

	for _, a := range guard.Accounts {
		if err := requireAccountKey(ctx, tx, a.AccountID, a.IdentitySignPub); err != nil {
			return Group{}, err
		}
	}

	if err := insertLogEntry(ctx, tx, entry); err != nil {
		return Group{}, err
	}

	for _, m := range diff.Add {
		// A plain insert: adding an account that is already a member conflicts.
		_, err := tx.Exec(ctx, `INSERT INTO memberships (group_id, account_id, identity_sign_pub, role) VALUES ($1, $2, $3, $4)`,
			entry.GroupID, m.AccountID, m.IdentitySignPub, m.Role)
		if isUniqueViolation(err) {
			return Group{}, ErrConflict
		}
		if err != nil {
			return Group{}, fmt.Errorf("add membership: %w", err)
		}
	}
	if err := applyInviteChange(ctx, tx, entry.GroupID, diff.Invite); err != nil {
		return Group{}, err
	}

	for _, accountID := range diff.Remove {
		if _, err := tx.Exec(ctx, `DELETE FROM memberships WHERE group_id = $1 AND account_id = $2`,
			entry.GroupID, accountID); err != nil {
			return Group{}, fmt.Errorf("remove membership: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Group{}, fmt.Errorf("commit append entry: %w", err)
	}
	return updated, nil
}

// applyInviteChange updates the invite mirror and, for a closed invite,
// its package row, so the server stops handing out a package whose list
// key is no longer meant to be usable.
func applyInviteChange(ctx context.Context, tx pgx.Tx, groupID string, c InviteChange) error {
	if c.Open != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO group_invites (group_id, invite_pub, expires_at, open) VALUES ($1, $2, $3, true)`,
			groupID, c.Open.InvitePub, c.Open.ExpiresAt); err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return fmt.Errorf("open invite: %w", err)
		}
	}
	if c.Close != nil {
		if _, err := tx.Exec(ctx, `UPDATE group_invites SET open = false WHERE group_id = $1 AND invite_pub = $2`,
			groupID, c.Close); err != nil {
			return fmt.Errorf("close invite: %w", err)
		}
		column := "revoked_at"
		if c.Joined {
			column = "consumed_at"
		}
		if _, err := tx.Exec(ctx, `UPDATE invites SET `+column+` = now()
			WHERE group_id = $1 AND invite_pub = $2 AND consumed_at IS NULL AND revoked_at IS NULL`,
			groupID, c.Close); err != nil {
			return fmt.Errorf("close invite package: %w", err)
		}
	}
	if c.VoidAll {
		if _, err := tx.Exec(ctx, `UPDATE group_invites SET open = false WHERE group_id = $1 AND open`, groupID); err != nil {
			return fmt.Errorf("void invites: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE invites SET revoked_at = now()
			WHERE group_id = $1 AND invite_pub IS NOT NULL AND consumed_at IS NULL AND revoked_at IS NULL`,
			groupID); err != nil {
			return fmt.Errorf("void invite packages: %w", err)
		}
	}
	return nil
}

// ListInvites returns every link invite the list's log has opened.
func (g *Groups) ListInvites(ctx context.Context, groupID string) ([]LogInvite, error) {
	rows, err := g.pool.Query(ctx, `SELECT invite_pub, expires_at, open FROM group_invites WHERE group_id = $1`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()

	var out []LogInvite
	for rows.Next() {
		var li LogInvite
		if err := rows.Scan(&li.InvitePub, &li.ExpiresAt, &li.Open); err != nil {
			return nil, fmt.Errorf("scan invite: %w", err)
		}
		out = append(out, li)
	}
	return out, rows.Err()
}

func insertLogEntry(ctx context.Context, tx pgx.Tx, e LogEntry) error {
	if _, err := tx.Exec(ctx, `INSERT INTO group_log (group_id, seq, entry, sig, hash, ts)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		e.GroupID, e.Seq, e.Entry, e.Sig, e.Hash, e.Ts); err != nil {
		return fmt.Errorf("insert log entry: %w", err)
	}
	return nil
}

// requireAccountKey locks the account row and checks it exists and still
// holds identityKey: ErrConflict otherwise (it was deleted since the
// caller read it).
func requireAccountKey(ctx context.Context, tx pgx.Tx, accountID string, identityKey []byte) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT true FROM accounts
		WHERE id = $1 AND identity_sign_pub = $2 FOR SHARE`, accountID, identityKey).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("lock account: %w", err)
	}
	return nil
}

// MemberLog returns at most limit entries from fromSeq on, if accountID
// can access the list. Access and the read share one
// snapshot. Read one after the other, a list deletion committing in
// between would pass the check and then serve an empty log, which the
// member's client would rightly report as a rollback (E2EE §4.4).
func (g *Groups) MemberLog(ctx context.Context, groupID, accountID string, fromSeq int64, limit int) ([]LogEntry, error) {
	var out []LogEntry
	err := readSnapshot(ctx, g.pool, func(tx pgx.Tx) error {
		if _, _, err := access(ctx, tx, groupID, accountID); err != nil {
			return err
		}
		var err error
		out, err = listLogFrom(ctx, tx, groupID, fromSeq, limit)
		return err
	})
	return out, err
}

func listLogFrom(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, groupID string, fromSeq int64, limit int) ([]LogEntry, error) {
	rows, err := q.Query(ctx, `SELECT group_id, seq, entry, sig, hash, ts FROM group_log
		WHERE group_id = $1 AND seq >= $2 ORDER BY seq LIMIT $3`, groupID, fromSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("list log: %w", err)
	}
	defer rows.Close()

	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.GroupID, &e.Seq, &e.Entry, &e.Sig, &e.Hash, &e.Ts); err != nil {
			return nil, fmt.Errorf("scan log entry: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// readSnapshot runs read in a read-only transaction that sees one snapshot
// throughout, so a deletion commits either before all of it or after.
func readSnapshot(ctx context.Context, pool *pgxpool.Pool, read func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := read(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (g *Groups) GetLogEntry(ctx context.Context, groupID string, seq int64) (LogEntry, error) {
	var e LogEntry
	err := g.pool.QueryRow(ctx, `SELECT group_id, seq, entry, sig, hash, ts FROM group_log
		WHERE group_id = $1 AND seq = $2`, groupID, seq,
	).Scan(&e.GroupID, &e.Seq, &e.Entry, &e.Sig, &e.Hash, &e.Ts)
	if errors.Is(err, pgx.ErrNoRows) {
		return LogEntry{}, ErrNotFound
	}
	if err != nil {
		return LogEntry{}, fmt.Errorf("get log entry: %w", err)
	}
	return e, nil
}

// Delete deletes a list and everything in it at once, for every member
// (API Reference). An account's current Inbox can't be deleted:
// ErrForbidden. accountID must still be the owner, re-checked under the
// list's row lock.
func (g *Groups) Delete(ctx context.Context, id, accountID string) error {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete group: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, role, err := lockedAccess(ctx, tx, id, accountID, "FOR UPDATE")
	if err != nil {
		return err
	}
	if role != "owner" {
		return ErrForbidden
	}
	if inbox, err := isInbox(ctx, tx, id); err != nil {
		return err
	} else if inbox {
		return ErrForbidden
	}
	if err := deleteList(ctx, tx, id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete group: %w", err)
	}
	return nil
}

// deleteList removes a list with its updates, snapshots, invites and their
// join requests, memberships and log, inside the caller's transaction,
// which holds the list's row FOR UPDATE: no append, write or new invite
// can commit alongside. The invites are locked first, so a join request
// racing in on one waits and then finds the invite gone.
func deleteList(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM invites WHERE group_id = $1 FOR UPDATE`, id); err != nil {
		return fmt.Errorf("lock invites of deleted list: %w", err)
	}
	for _, stmt := range []string{
		`DELETE FROM list_updates WHERE list_id = $1`,
		`DELETE FROM list_snapshots WHERE list_id = $1`,
		`DELETE FROM join_requests WHERE invite_id IN (SELECT id FROM invites WHERE group_id = $1)`,
		`DELETE FROM invites WHERE group_id = $1`,
		`DELETE FROM group_invites WHERE group_id = $1`,
		`DELETE FROM memberships WHERE group_id = $1`,
		`DELETE FROM group_log WHERE group_id = $1`,
		`DELETE FROM groups WHERE id = $1`,
	} {
		if _, err := tx.Exec(ctx, stmt, id); err != nil {
			return fmt.Errorf("delete list: %w", err)
		}
	}
	return nil
}

// Access is the one list-access check every HTTP and WebSocket path
// uses: the list exists and accountID is a member. Returns the list and
// the caller's role in it. Anything else — unknown or deleted list, not a
// member — is ErrForbidden, so ids can't be probed.
func (g *Groups) Access(ctx context.Context, groupID, accountID string) (Group, string, error) {
	return access(ctx, g.pool, groupID, accountID)
}

func access(ctx context.Context, q querier, groupID, accountID string) (Group, string, error) {
	var grp Group
	var role string
	err := q.QueryRow(ctx, `SELECT `+groupColumnsG+`, m.role FROM `+accessibleMembers+`
		  AND g.id = $1 AND m.account_id = $2`,
		groupID, accountID,
	).Scan(&grp.ID, &grp.HeadSeq, &grp.HeadHash, &grp.Epoch, &grp.CreatedAt, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, "", ErrForbidden
	}
	if err != nil {
		return Group{}, "", fmt.Errorf("check group access: %w", err)
	}
	return grp, role, nil
}

// lockedAccess locks the list row (lock is "FOR SHARE", "FOR NO KEY UPDATE" or "FOR UPDATE")
// and runs access() under that lock. It conflicts with the UPDATE every
// membership append, rotation and list deletion does to the row, so none
// of those can commit between this check and the caller's write.
func lockedAccess(ctx context.Context, tx pgx.Tx, groupID, accountID, lock string) (Group, string, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT true FROM groups WHERE id = $1 `+lock, groupID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, "", ErrForbidden
	}
	if err != nil {
		return Group{}, "", fmt.Errorf("lock group: %w", err)
	}
	return access(ctx, tx, groupID, accountID)
}

// AccessibleAccountIDs returns every account that passes Access for the
// list right now: the audience for its `group` notifications.
func (g *Groups) AccessibleAccountIDs(ctx context.Context, groupID string) ([]string, error) {
	rows, err := g.pool.Query(ctx, `SELECT m.account_id FROM `+accessibleMembers+` AND g.id = $1`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list accessible accounts: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ListMembersWithKeys returns the group's current mirrored membership with
// the identity key each member was admitted under — what membership-log
// validation actually keys on (E2EE §4.2 rules are stated in terms of
// identity keys, not account ids).
func (g *Groups) ListMembersWithKeys(ctx context.Context, groupID string) ([]MemberView, error) {
	rows, err := g.pool.Query(ctx, `SELECT account_id, identity_sign_pub, role
		FROM memberships WHERE group_id = $1`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var out []MemberView
	for rows.Next() {
		var mv MemberView
		if err := rows.Scan(&mv.AccountID, &mv.IdentitySignPub, &mv.Role); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, mv)
	}
	return out, rows.Err()
}

// AuthorizedWrite runs write in one transaction that first re-checks,
// under the list's row lock, everything a content write depends on:
// the list exists, accountID is a member, and epoch is current. The lock
// conflicts with the UPDATE every membership append, rotation and list
// deletion does to the row, so none of those can commit between this check
// and the write. It also serializes the list's content writers on every
// instance (Updates.Append bumps the row's seq counter): taken FOR SHARE,
// two writers would both upgrade to the counter's row lock and deadlock.
// NO KEY UPDATE still lets foreign-key checks on the row through.
func (g *Groups) AuthorizedWrite(ctx context.Context, listID, accountID string, epoch int64, write func(tx pgx.Tx) error) error {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin list write: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	list, _, err := lockedAccess(ctx, tx, listID, accountID, "FOR NO KEY UPDATE")
	if err != nil {
		return err
	}
	if list.Epoch != epoch {
		return ErrStaleEpoch
	}
	if err := write(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit list write: %w", err)
	}
	return nil
}
