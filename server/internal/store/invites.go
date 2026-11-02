package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Invite struct {
	ID         string
	GroupID    string
	CreatedBy  string
	Package    []byte
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
	// InvitePub is set on a link invite (E2EE §6): the key its log
	// `invite` entry opened. Nil on an approve-first invite.
	InvitePub []byte
}

// Usable reports whether the invite can still be joined: not expired, not
// consumed, not revoked (E2EE §6: "Invites expire after 7 days and are
// single-use."). The owner can revoke pending invites.
func (i Invite) Usable(now time.Time) bool {
	return i.ConsumedAt == nil && i.RevokedAt == nil && now.Before(i.ExpiresAt)
}

type JoinRequest struct {
	ID             string
	InviteID       string
	AccountID      string
	EncDisplayName []byte
	MAC            []byte
	CreatedAt      time.Time
	ApprovedAt     *time.Time
}

type Invites struct {
	pool *pgxpool.Pool
}

func NewInvites(pool *pgxpool.Pool) *Invites {
	return &Invites{pool: pool}
}

const inviteColumns = `id, group_id, created_by, package, expires_at, consumed_at, revoked_at, created_at, invite_pub`

func scanInvite(row pgx.Row) (Invite, error) {
	var i Invite
	err := row.Scan(&i.ID, &i.GroupID, &i.CreatedBy, &i.Package, &i.ExpiresAt, &i.ConsumedAt, &i.RevokedAt, &i.CreatedAt, &i.InvitePub)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, ErrNotFound
	}
	if err != nil {
		return Invite{}, fmt.Errorf("scan invite: %w", err)
	}
	return i, nil
}

// Create stores an invite if createdBy is still the owner of the live
// list, re-checked under its row lock: a deletion that commits first
// denies it, and a deletion that commits after it
// revokes it. A link invite (invitePub set) also needs its log
// `invite` entry to be open and to expire no earlier than expiresAt:
// ErrInviteNotUsable otherwise.
func (iv *Invites) Create(ctx context.Context, id, groupID, createdBy string, pkg []byte, expiresAt time.Time, invitePub []byte) (Invite, error) {
	tx, err := iv.pool.Begin(ctx)
	if err != nil {
		return Invite{}, fmt.Errorf("begin create invite: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, role, err := lockedAccess(ctx, tx, groupID, createdBy, "FOR SHARE")
	if err != nil {
		return Invite{}, err
	}
	if role != "owner" {
		return Invite{}, ErrForbidden
	}
	if invitePub != nil {
		var open bool
		err := tx.QueryRow(ctx, `SELECT open AND expires_at >= $3 FROM group_invites
			WHERE group_id = $1 AND invite_pub = $2`, groupID, invitePub, expiresAt).Scan(&open)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !open) {
			return Invite{}, ErrInviteNotUsable
		}
		if err != nil {
			return Invite{}, fmt.Errorf("check log invite: %w", err)
		}
	}
	row := tx.QueryRow(ctx, `INSERT INTO invites (id, group_id, created_by, package, expires_at, invite_pub)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+inviteColumns,
		id, groupID, createdBy, pkg, expiresAt, invitePub)
	inv, err := scanInvite(row)
	if isUniqueViolation(err) {
		return Invite{}, ErrConflict
	}
	if err != nil {
		return Invite{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Invite{}, fmt.Errorf("commit create invite: %w", err)
	}
	return inv, nil
}

func (iv *Invites) Get(ctx context.Context, id string) (Invite, error) {
	row := iv.pool.QueryRow(ctx, `SELECT `+inviteColumns+` FROM invites WHERE id = $1`, id)
	return scanInvite(row)
}

func (iv *Invites) Revoke(ctx context.Context, id, groupID string) error {
	tag, err := iv.pool.Exec(ctx, `UPDATE invites SET revoked_at = now()
		WHERE id = $1 AND group_id = $2 AND revoked_at IS NULL AND consumed_at IS NULL`, id, groupID)
	if err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// consumeInvite marks the invite consumed and the join request approved,
// inside Groups.AppendEntry's transaction (E2EE §6: "An invite is consumed
// when the add entry referencing it is appended" — the entry's
// invite_proof IS the join request's MAC). The request must come from the
// account being added, and the invite must still be usable; the row lock
// serializes this against Revoke and against a second approval.
func consumeInvite(ctx context.Context, tx pgx.Tx, groupID string, mac []byte, subjectAccountID string) error {
	var inviteID, requestID string
	err := tx.QueryRow(ctx, `SELECT i.id, jr.id FROM join_requests jr
		JOIN invites i ON i.id = jr.invite_id
		WHERE i.group_id = $1 AND jr.mac = $2 AND jr.account_id = $3 AND jr.approved_at IS NULL
		  AND i.consumed_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()
		LIMIT 1
		FOR UPDATE OF i, jr`, groupID, mac, subjectAccountID).Scan(&inviteID, &requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInviteNotUsable
	}
	if err != nil {
		return fmt.Errorf("find invite by mac: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE invites SET consumed_at = now() WHERE id = $1`, inviteID); err != nil {
		return fmt.Errorf("consume invite: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE join_requests SET approved_at = now() WHERE id = $1`, requestID); err != nil {
		return fmt.Errorf("approve join request: %w", err)
	}
	return nil
}

// CreateJoinRequest records a join request if the invite is still usable
// and its group live, checked in the same statement under the invite's row
// lock, so a revocation or group deletion can't slip between check and
// insert. An account has at most one pending request per invite; a
// repeat replaces it. ErrInviteNotUsable if the invite isn't usable.
func (iv *Invites) CreateJoinRequest(ctx context.Context, inviteID, accountID string, encDisplayName, mac []byte) (JoinRequest, error) {
	var jr JoinRequest
	err := iv.pool.QueryRow(ctx, `INSERT INTO join_requests (invite_id, account_id, enc_display_name, mac)
		SELECT i.id, $2, $3, $4 FROM invites i JOIN groups g ON g.id = i.group_id
		WHERE i.id = $1 AND i.consumed_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()
		  AND g.deleted_at IS NULL
		FOR SHARE OF i
		ON CONFLICT (invite_id, account_id) WHERE approved_at IS NULL
		DO UPDATE SET enc_display_name = EXCLUDED.enc_display_name, mac = EXCLUDED.mac, created_at = now()
		RETURNING id, invite_id, account_id, enc_display_name, mac, created_at, approved_at`,
		inviteID, accountID, encDisplayName, mac,
	).Scan(&jr.ID, &jr.InviteID, &jr.AccountID, &jr.EncDisplayName, &jr.MAC, &jr.CreatedAt, &jr.ApprovedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return JoinRequest{}, ErrInviteNotUsable
	}
	if err != nil {
		return JoinRequest{}, fmt.Errorf("create join request: %w", err)
	}
	return jr, nil
}

// PendingJoinRequest is a join request still awaiting approval, with the
// requester's registered identity keys.
type PendingJoinRequest struct {
	JoinRequest
	IdentitySignPub  []byte
	IdentityAgreePub []byte
}

// ListJoinRequestsForGroup returns requests the owner can still approve: not
// approved, on an invite that is unexpired, unrevoked and unconsumed, from
// an account that still exists.
func (iv *Invites) ListJoinRequestsForGroup(ctx context.Context, groupID string) ([]PendingJoinRequest, error) {
	rows, err := iv.pool.Query(ctx, `SELECT jr.id, jr.invite_id, jr.account_id, jr.enc_display_name, jr.mac, jr.created_at, jr.approved_at,
			a.identity_sign_pub, a.identity_agree_pub
		FROM join_requests jr
		JOIN invites i ON i.id = jr.invite_id
		JOIN accounts a ON a.id = jr.account_id
		WHERE i.group_id = $1 AND jr.approved_at IS NULL
		  AND i.consumed_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()
		  AND a.deleted_at IS NULL
		ORDER BY jr.created_at`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list join requests: %w", err)
	}
	defer rows.Close()

	var out []PendingJoinRequest
	for rows.Next() {
		var jr PendingJoinRequest
		if err := rows.Scan(&jr.ID, &jr.InviteID, &jr.AccountID, &jr.EncDisplayName, &jr.MAC, &jr.CreatedAt, &jr.ApprovedAt,
			&jr.IdentitySignPub, &jr.IdentityAgreePub); err != nil {
			return nil, fmt.Errorf("scan join request: %w", err)
		}
		out = append(out, jr)
	}
	return out, rows.Err()
}
