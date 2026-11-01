-- Problems are owned by one user; a problem row is the mutable handle (status,
-- current_version, soft-delete marker) and problem_versions holds the
-- immutable content. created_by is the actor recorded as three columns.
CREATE TABLE problems (
    id                    TEXT COLLATE "C" PRIMARY KEY,
    owner_id              TEXT COLLATE "C" NOT NULL REFERENCES users (id),
    status                TEXT NOT NULL CHECK (status IN ('draft', 'live', 'archived')),
    current_version       INTEGER NOT NULL CHECK (current_version >= 1),
    created_by_kind       TEXT NOT NULL CHECK (created_by_kind IN ('human', 'agent', 'system')),
    created_by_user_id    TEXT NOT NULL DEFAULT '',
    created_by_client_id  TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL,
    deleted_at            TIMESTAMPTZ
);

CREATE INDEX problems_owner ON problems (owner_id, deleted_at, id);

-- Versions are immutable: the triggers below reject any UPDATE or DELETE.
CREATE TABLE problem_versions (
    problem_id            TEXT COLLATE "C" NOT NULL REFERENCES problems (id),
    version               INTEGER NOT NULL CHECK (version >= 1),
    type                  TEXT NOT NULL CHECK (type IN ('mcq', 'cloze', 'open')),
    prompt                TEXT NOT NULL,
    payload               JSONB NOT NULL,
    explanation           TEXT NOT NULL DEFAULT '',
    source                TEXT NOT NULL DEFAULT '',
    change_kind           TEXT NOT NULL CHECK (change_kind IN ('minor', 'major')),
    created_by_kind       TEXT NOT NULL CHECK (created_by_kind IN ('human', 'agent', 'system')),
    created_by_user_id    TEXT NOT NULL DEFAULT '',
    created_by_client_id  TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (problem_id, version)
);

CREATE FUNCTION problem_versions_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'problem_versions are immutable';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER problem_versions_no_update BEFORE UPDATE ON problem_versions
    FOR EACH ROW EXECUTE FUNCTION problem_versions_immutable();

CREATE TRIGGER problem_versions_no_delete BEFORE DELETE ON problem_versions
    FOR EACH ROW EXECUTE FUNCTION problem_versions_immutable();
