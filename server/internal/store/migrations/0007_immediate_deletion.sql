-- Deleting a list or an account now removes it at once, so nothing is
-- soft-deleted any more and no retention window remains.

-- Who wrote an update or created an invite was recorded but never read.
-- Members verify authorship from the signed header (E2EE §7), and with
-- these gone no history row points at an account, so its row can go.
ALTER TABLE list_updates DROP COLUMN author_account_id;
ALTER TABLE invites DROP COLUMN created_by;

-- A server from before 0004 inserts author_account_id, so none can run
-- against this schema: the seq it relied on is no longer needed.
DROP TRIGGER list_updates_fill_seq ON list_updates;
DROP FUNCTION list_updates_fill_seq();

-- Lists already deleted, and everything that hangs off them, children
-- first. invite_join_budget goes with its invite (ON DELETE CASCADE) and
-- an Inbox's accounts.inbox_id is cleared (ON DELETE SET NULL).
CREATE TEMPORARY TABLE deleted_lists ON COMMIT DROP AS
    SELECT id FROM groups WHERE deleted_at IS NOT NULL;
DELETE FROM list_updates WHERE list_id IN (SELECT id FROM deleted_lists);
DELETE FROM list_snapshots WHERE list_id IN (SELECT id FROM deleted_lists);
DELETE FROM join_requests WHERE invite_id IN
    (SELECT id FROM invites WHERE group_id IN (SELECT id FROM deleted_lists));
DELETE FROM invites WHERE group_id IN (SELECT id FROM deleted_lists);
DELETE FROM group_invites WHERE group_id IN (SELECT id FROM deleted_lists);
DELETE FROM memberships WHERE group_id IN (SELECT id FROM deleted_lists);
DELETE FROM group_log WHERE group_id IN (SELECT id FROM deleted_lists);
DELETE FROM groups WHERE id IN (SELECT id FROM deleted_lists);

-- Accounts already deleted, including those an older retention job could
-- not remove because history they authored still pointed at them. A
-- deleted account is in no list (it had to leave every list it didn't
-- own, and the lists it owned are gone above).
DELETE FROM sessions WHERE account_id IN (SELECT id FROM accounts WHERE deleted_at IS NOT NULL);
DELETE FROM join_requests WHERE account_id IN (SELECT id FROM accounts WHERE deleted_at IS NOT NULL);
DELETE FROM challenges WHERE kind = 'rekey' AND subject IN
    (SELECT id::text FROM accounts WHERE deleted_at IS NOT NULL);
DELETE FROM accounts WHERE deleted_at IS NOT NULL;

ALTER TABLE groups DROP COLUMN deleted_at;
ALTER TABLE accounts DROP COLUMN deleted_at;
