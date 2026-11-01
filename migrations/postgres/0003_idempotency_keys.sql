-- Stored responses for POST requests that carried an Idempotency-Key. The key
-- is scoped to the owning user. request_hash fingerprints method, path and
-- body so a reused key with a different request can be rejected. Rows are
-- replayed until expires_at and removed by the expiry sweep.
CREATE TABLE idempotency_keys (
    user_id      TEXT COLLATE "C" NOT NULL REFERENCES users (id),
    key          TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status       INTEGER NOT NULL,
    headers      JSONB NOT NULL,
    body         BYTEA NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, key)
);

CREATE INDEX idempotency_keys_expires_at ON idempotency_keys (expires_at);
