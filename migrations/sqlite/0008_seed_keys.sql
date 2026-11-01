-- Seed keys map the stable key of an item in a seed file to the row it
-- created, per owner. digest is the hash of the item as last applied, so a
-- re-run changes a row only when the file's item changed since. A key row
-- outlives a soft-deleted entity, which is how seeding knows not to bring a
-- deleted item back.
CREATE TABLE seed_keys (
    owner_id    TEXT NOT NULL REFERENCES users (id),
    kind        TEXT NOT NULL CHECK (kind IN ('tag', 'problem', 'quiz')),
    key         TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    digest      TEXT NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (owner_id, kind, key)
);
