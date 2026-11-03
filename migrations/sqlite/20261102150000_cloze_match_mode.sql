-- The cloze blank flag `case_sensitive` became a named `match` mode. Both old
-- values map to "relaxed" (the old true has no equivalent; relaxed accepts
-- everything it accepted). Rewrite every stored cloze payload that still has
-- the old key. problem_versions are immutable, so the guard triggers are
-- dropped for the rewrite and recreated unchanged. Attempts are not touched.
DROP TRIGGER problem_versions_no_update;

UPDATE problem_versions
SET payload = json_set(payload, '$.blanks', json((
    SELECT json_group_array(
        CASE WHEN json_type(value) = 'object'
        THEN json_set(json_remove(value, '$.case_sensitive'), '$.match',
            CASE WHEN json_extract(value, '$.match') IN ('relaxed', 'case-insensitive')
                 THEN json_extract(value, '$.match') ELSE 'relaxed' END)
        ELSE json(value) END)
    FROM json_each(payload, '$.blanks'))))
WHERE type = 'cloze'
  AND json_valid(payload)
  AND json_type(payload, '$.blanks') = 'array'
  AND EXISTS (
    SELECT 1 FROM json_each(payload, '$.blanks')
    WHERE json_type(value) = 'object' AND json_type(value, '$.case_sensitive') IS NOT NULL);

CREATE TRIGGER problem_versions_no_update BEFORE UPDATE ON problem_versions
BEGIN
    SELECT RAISE(ABORT, 'problem_versions are immutable');
END;
