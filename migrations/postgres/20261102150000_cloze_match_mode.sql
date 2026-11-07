-- The cloze blank flag `case_sensitive` became a named `match` mode. Both old
-- values map to "relaxed" (the old true has no equivalent; relaxed accepts
-- everything it accepted). Rewrite every stored cloze payload that still has
-- the old key. problem_versions are immutable, so the guard trigger is
-- disabled for the rewrite and enabled again. Attempts are not touched.
ALTER TABLE problem_versions DISABLE TRIGGER problem_versions_no_update;

UPDATE problem_versions
SET payload = jsonb_set(payload, '{blanks}', (
    SELECT jsonb_agg(
        CASE WHEN jsonb_typeof(b) = 'object'
        THEN (b - 'case_sensitive') || jsonb_build_object('match',
            CASE WHEN b ->> 'match' IN ('relaxed', 'case-insensitive')
                 THEN b ->> 'match' ELSE 'relaxed' END)
        ELSE b END
        ORDER BY ord)
    FROM jsonb_array_elements(payload -> 'blanks') WITH ORDINALITY AS t (b, ord)))
WHERE type = 'cloze'
  AND jsonb_typeof(payload -> 'blanks') = 'array'
  AND EXISTS (
    SELECT 1 FROM jsonb_array_elements(payload -> 'blanks') AS e
    WHERE jsonb_typeof(e) = 'object' AND (e -> 'case_sensitive') IS NOT NULL);

ALTER TABLE problem_versions ENABLE TRIGGER problem_versions_no_update;
