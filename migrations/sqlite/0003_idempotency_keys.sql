-- Stored responses for POST requests that carried an Idempotency-Key. The key
-- is scoped to the owning user. request_hash fingerprints method, path and
-- body so a reused key with a different request can be rejected. Rows are
-- replayed until expires_at and removed by the expiry sweep.
CREATE TABLE idempotency_keys (
    user_id      TEXT NOT NULL REFERENCES users (id),
    key          TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status       INTEGER NOT NULL,
    headers      TEXT NOT NULL,
    body         BLOB NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    PRIMARY KEY (user_id, key)
);

CREATE INDEX idempotency_keys_expires_at ON idempotency_keys (expires_at);
