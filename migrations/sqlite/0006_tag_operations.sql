-- Audit trail for tag evolution. payload and before_snapshot are JSON text.
-- before_snapshot is set when the operation is applied and never rewritten.
-- Actors are stored as kind/user_id/client_id columns; decided_by_kind,
-- decided_at and applied_at are NULL while the operation is proposed.
CREATE TABLE tag_operations (
    id                    TEXT PRIMARY KEY,
    owner_id              TEXT NOT NULL REFERENCES users (id),
    kind                  TEXT NOT NULL CHECK (kind IN ('rename', 'merge', 'split')),
    payload               TEXT NOT NULL,
    status                TEXT NOT NULL CHECK (status IN ('proposed', 'applied', 'rejected')),
    proposed_by_kind      TEXT NOT NULL CHECK (proposed_by_kind IN ('human', 'agent', 'system')),
    proposed_by_user_id   TEXT NOT NULL DEFAULT '',
    proposed_by_client_id TEXT NOT NULL DEFAULT '',
    decided_by_kind       TEXT CHECK (decided_by_kind IN ('human', 'agent', 'system')),
    decided_by_user_id    TEXT NOT NULL DEFAULT '',
    decided_by_client_id  TEXT NOT NULL DEFAULT '',
    before_snapshot       TEXT,
    created_at            INTEGER NOT NULL,
    decided_at            INTEGER,
    applied_at            INTEGER,
    CHECK ((status = 'proposed') = (decided_at IS NULL)),
    CHECK ((status = 'applied') = (applied_at IS NOT NULL AND before_snapshot IS NOT NULL))
);

CREATE INDEX tag_operations_owner ON tag_operations (owner_id, id);
