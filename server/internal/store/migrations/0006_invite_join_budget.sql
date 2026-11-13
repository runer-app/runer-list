-- The join limit per invite (docs/self-hosting.md, Limits) as a token
-- bucket shared by every server instance: an invite is a credential, so N
-- instances must not allow N times the attempts. Join requests and
-- link-invite accepts both spend from it.
CREATE TABLE invite_join_budget (
    invite_id  TEXT PRIMARY KEY REFERENCES invites(id) ON DELETE CASCADE,
    tokens     DOUBLE PRECISION NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
