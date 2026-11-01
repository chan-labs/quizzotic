-- Users are references to (issuer, subject) identities; profile fields are
-- copied from token claims and informational only. The scheduling columns are
-- per-user overrides of the config defaults; NULL means "use the default".
CREATE TABLE users (
    id                  TEXT PRIMARY KEY,
    display_name        TEXT NOT NULL DEFAULT '',
    email               TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    desired_retention   REAL,
    learning_steps      TEXT,
    new_per_day         INTEGER,
    max_reviews_per_day INTEGER,
    new_interval        INTEGER,
    weak_tag_boost      REAL,
    rollover_hour       INTEGER,
    timezone            TEXT
);

CREATE TABLE identities (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id),
    issuer       TEXT NOT NULL,
    subject      TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    UNIQUE (issuer, subject)
);

CREATE INDEX identities_user_id ON identities (user_id);

-- Server-side web sessions. Only a hash of the cookie token is stored.
CREATE TABLE web_sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id),
    token_hash   TEXT NOT NULL UNIQUE,
    csrf_token   TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);

CREATE INDEX web_sessions_user_id ON web_sessions (user_id);
CREATE INDEX web_sessions_expires_at ON web_sessions (expires_at);
