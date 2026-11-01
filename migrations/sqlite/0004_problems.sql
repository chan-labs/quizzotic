-- Problems are owned by one user; a problem row is the mutable handle (status,
-- current_version, soft-delete marker) and problem_versions holds the
-- immutable content. created_by is the actor recorded as three columns.
CREATE TABLE problems (
    id                    TEXT PRIMARY KEY,
    owner_id              TEXT NOT NULL REFERENCES users (id),
    status                TEXT NOT NULL CHECK (status IN ('draft', 'live', 'archived')),
    current_version       INTEGER NOT NULL CHECK (current_version >= 1),
    created_by_kind       TEXT NOT NULL CHECK (created_by_kind IN ('human', 'agent', 'system')),
    created_by_user_id    TEXT NOT NULL DEFAULT '',
    created_by_client_id  TEXT NOT NULL DEFAULT '',
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    deleted_at            INTEGER
);

CREATE INDEX problems_owner ON problems (owner_id, deleted_at, id);

-- Versions are immutable: the triggers below reject any UPDATE or DELETE.
CREATE TABLE problem_versions (
    problem_id            TEXT NOT NULL REFERENCES problems (id),
    version               INTEGER NOT NULL CHECK (version >= 1),
    type                  TEXT NOT NULL CHECK (type IN ('mcq', 'cloze', 'open')),
    prompt                TEXT NOT NULL,
    payload               TEXT NOT NULL,
    explanation           TEXT NOT NULL DEFAULT '',
    source                TEXT NOT NULL DEFAULT '',
    change_kind           TEXT NOT NULL CHECK (change_kind IN ('minor', 'major')),
    created_by_kind       TEXT NOT NULL CHECK (created_by_kind IN ('human', 'agent', 'system')),
    created_by_user_id    TEXT NOT NULL DEFAULT '',
    created_by_client_id  TEXT NOT NULL DEFAULT '',
    created_at            INTEGER NOT NULL,
    PRIMARY KEY (problem_id, version)
);

CREATE TRIGGER problem_versions_no_update BEFORE UPDATE ON problem_versions
BEGIN
    SELECT RAISE(ABORT, 'problem_versions are immutable');
END;

CREATE TRIGGER problem_versions_no_delete BEFORE DELETE ON problem_versions
BEGIN
    SELECT RAISE(ABORT, 'problem_versions are immutable');
END;
