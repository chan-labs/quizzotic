-- Tags are owned by one user. A slug is unique among the owner's active
-- tags; old slugs live on as aliases (unique per owner), so every reference
-- by slug resolves to exactly one tag. Alias rows carry owner_id for that
-- uniqueness. problem_tags attaches tags to the problem, not a version.
CREATE TABLE tags (
    id           TEXT PRIMARY KEY,
    owner_id     TEXT NOT NULL REFERENCES users (id),
    slug         TEXT NOT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL CHECK (status IN ('active', 'merged', 'retired')),
    merged_into  TEXT REFERENCES tags (id),
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE UNIQUE INDEX tags_owner_slug_active ON tags (owner_id, slug) WHERE status = 'active';
CREATE INDEX tags_owner ON tags (owner_id, id);

CREATE TABLE tag_aliases (
    owner_id  TEXT NOT NULL REFERENCES users (id),
    slug      TEXT NOT NULL,
    tag_id    TEXT NOT NULL REFERENCES tags (id),
    PRIMARY KEY (owner_id, slug)
);

CREATE INDEX tag_aliases_tag ON tag_aliases (tag_id);

CREATE TABLE problem_tags (
    problem_id  TEXT NOT NULL REFERENCES problems (id),
    tag_id      TEXT NOT NULL REFERENCES tags (id),
    PRIMARY KEY (problem_id, tag_id)
);

CREATE INDEX problem_tags_tag ON problem_tags (tag_id, problem_id);
