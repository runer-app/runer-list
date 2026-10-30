-- Only the Devices list read it, and that list is gone.
ALTER TABLE sessions DROP COLUMN user_agent;
