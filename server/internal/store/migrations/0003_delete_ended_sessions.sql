-- A session is deleted when it ends, so nothing needs to remember that it did.
DELETE FROM sessions WHERE revoked_at IS NOT NULL;
ALTER TABLE sessions DROP COLUMN revoked_at;
