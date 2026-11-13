-- Single-use login and rekey nonces (E2EE §3.4, §9.2), in the database so
-- a challenge issued by one server instance can be answered at another.
-- subject is the base64 auth_pub for a login, the account id for a rekey.
CREATE TABLE challenges (
    kind       TEXT NOT NULL CHECK (kind IN ('login', 'rekey')),
    subject    TEXT NOT NULL,
    nonce      BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (kind, subject, nonce)
);

CREATE INDEX challenges_expires_at_idx ON challenges (expires_at);
