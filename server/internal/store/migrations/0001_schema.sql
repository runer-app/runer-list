-- The whole schema. What each table protects: protocol/docs/e2ee-protocol.md.

-- A list: its signed membership log and one encrypted document.
CREATE TABLE groups (
    id         TEXT PRIMARY KEY, -- hex SHA-256 of the genesis entry bytes
    head_seq   BIGINT NOT NULL,
    head_hash  BYTEA NOT NULL,
    epoch      BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE accounts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    auth_pub            BYTEA NOT NULL UNIQUE,
    identity_sign_pub   BYTEA NOT NULL UNIQUE,
    identity_agree_pub  BYTEA NOT NULL,
    identity_bundle     BYTEA NOT NULL,
    bundle_gen          INTEGER NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ,
    -- The account's Inbox (§2). SET NULL lets the retention job purge a deleted one.
    inbox_id            TEXT REFERENCES groups(id) ON DELETE SET NULL,
    -- The account's settings, sealed by the client; opaque (§3.3). NULL until first set.
    settings_sealed     BYTEA,
    settings_gen        INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE sessions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id    UUID NOT NULL REFERENCES accounts(id),
    token_hash    BYTEA NOT NULL UNIQUE,
    user_agent    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ
);

CREATE INDEX sessions_account_id_idx ON sessions(account_id);

CREATE TABLE group_log (
    group_id TEXT NOT NULL REFERENCES groups(id),
    seq      BIGINT NOT NULL,
    entry    BYTEA NOT NULL, -- exact signed bytes, stored verbatim, never re-serialized
    sig      BYTEA NOT NULL,
    hash     BYTEA NOT NULL, -- SHA-256(entry)
    ts       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (group_id, seq)
);

-- Mirror of current membership, derived from group_log, never authoritative
-- (E2EE §4.3: "the server mirrors the log, never the other way round").
-- identity_sign_pub is the key the log admitted.
CREATE TABLE memberships (
    group_id          TEXT NOT NULL REFERENCES groups(id),
    account_id        UUID NOT NULL REFERENCES accounts(id),
    identity_sign_pub BYTEA NOT NULL,
    role              TEXT NOT NULL CHECK (role IN ('owner', 'member')),
    PRIMARY KEY (group_id, account_id)
);

CREATE INDEX memberships_account_id_idx ON memberships(account_id);

-- Invites and join requests. See protocol/docs/e2ee-protocol.md §6.
CREATE TABLE invites (
    id          TEXT PRIMARY KEY, -- client-derived HKDF(s, "invite-id"); opaque to the server
    group_id    TEXT NOT NULL REFERENCES groups(id),
    created_by  UUID NOT NULL REFERENCES accounts(id),
    package     BYTEA NOT NULL,   -- opaque, AEAD-encrypted under the invite secret
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set on a link invite's package: the key its `invite` entry opened. The
    -- package holds the list key, so it stops being served once that invite
    -- closes.
    invite_pub  BYTEA
);

CREATE TABLE join_requests (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invite_id        TEXT NOT NULL REFERENCES invites(id),
    account_id       UUID NOT NULL REFERENCES accounts(id),
    enc_display_name BYTEA NOT NULL, -- opaque, AEAD-encrypted under the invite secret
    mac              BYTEA NOT NULL, -- HMAC(mac_key, invite_id || identity pubs); server relays, never verifies
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at      TIMESTAMPTZ
);

CREATE INDEX join_requests_invite_id_idx ON join_requests(invite_id);
-- One pending join request per account per invite; a repeat replaces it.
CREATE UNIQUE INDEX join_requests_pending_idx ON join_requests (invite_id, account_id) WHERE approved_at IS NULL;

-- Encrypted realtime sync. See protocol/docs/sync-protocol.md.
CREATE TABLE list_updates (
    list_id           TEXT NOT NULL REFERENCES groups(id),
    seq               BIGSERIAL,
    epoch             BIGINT NOT NULL,
    author_account_id UUID NOT NULL REFERENCES accounts(id),
    msg               BYTEA NOT NULL, -- the original {header, nonce, ct, sig} frame, opaque
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, seq)
);

CREATE TABLE list_snapshots (
    list_id    TEXT NOT NULL REFERENCES groups(id),
    epoch      BIGINT NOT NULL,
    covers_seq BIGINT NOT NULL,
    msg        BYTEA NOT NULL, -- opaque encrypted full document state
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, epoch)
);

-- Link invites (protocol/docs/e2ee-protocol.md §6). Mirror of every invite key a list's
-- log has opened, derived from group_log like memberships, never
-- authoritative. Closed rows stay: a key can't be opened twice (rule 9).
CREATE TABLE group_invites (
    group_id   TEXT NOT NULL REFERENCES groups(id),
    invite_pub BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    open       BOOLEAN NOT NULL,
    PRIMARY KEY (group_id, invite_pub)
);
