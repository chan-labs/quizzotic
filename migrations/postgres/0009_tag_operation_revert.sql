-- A reverted tag operation stays 'applied' (its before_snapshot is never
-- rewritten) and is marked with who reverted it and when. reverted_at is NULL
-- until the revert; the actor columns follow the decided_by_* convention.
ALTER TABLE tag_operations ADD COLUMN reverted_at TIMESTAMPTZ;
ALTER TABLE tag_operations ADD COLUMN reverted_by_kind TEXT CHECK (reverted_by_kind IN ('human', 'agent', 'system'));
ALTER TABLE tag_operations ADD COLUMN reverted_by_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tag_operations ADD COLUMN reverted_by_client_id TEXT NOT NULL DEFAULT '';
