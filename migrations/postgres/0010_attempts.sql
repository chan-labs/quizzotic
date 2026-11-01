-- Attempts are the append-only source of truth for learning. One row is one
-- answer. user_id is the owner; client_* is the actor that submitted it.
--
-- rating is the rating the client sent (NULL when it sent none). score is NULL
-- for an open attempt until it is rated; auto_correct is NULL for open
-- problems, which are not auto-graded. session_id has no foreign key: sessions
-- arrive with the scheduling milestone.
CREATE TABLE attempts (
    id                TEXT COLLATE "C" PRIMARY KEY,
    user_id           TEXT COLLATE "C" NOT NULL REFERENCES users (id),
    problem_id        TEXT COLLATE "C" NOT NULL REFERENCES problems (id),
    problem_version   INTEGER NOT NULL CHECK (problem_version >= 1),
    card_epoch        INTEGER NOT NULL CHECK (card_epoch >= 1),
    session_id        TEXT COLLATE "C",
    response          JSONB NOT NULL,
    auto_correct      BOOLEAN,
    score             DOUBLE PRECISION CHECK (score BETWEEN 0 AND 1),
    rating            INTEGER CHECK (rating BETWEEN 1 AND 4),
    latency_ms        INTEGER NOT NULL CHECK (latency_ms >= 0),
    answered_at       TIMESTAMPTZ NOT NULL,
    client_kind       TEXT NOT NULL CHECK (client_kind IN ('human', 'agent', 'system')),
    client_user_id    TEXT NOT NULL DEFAULT '',
    client_client_id  TEXT NOT NULL DEFAULT ''
);

CREATE INDEX attempts_user_problem ON attempts (user_id, problem_id, card_epoch, answered_at);
CREATE INDEX attempts_user_answered ON attempts (user_id, answered_at, id);

-- Attempts are never deleted.
CREATE FUNCTION attempts_no_delete() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'attempts are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER attempts_no_delete BEFORE DELETE ON attempts
    FOR EACH ROW EXECUTE FUNCTION attempts_no_delete();

-- The only permitted update is the one-time rating fill: a missing rating is
-- set, and for an open attempt the score that follows from it. Every other
-- column must stay as it was.
CREATE FUNCTION attempts_rating_fill_only() RETURNS trigger AS $$
BEGIN
    IF NOT (
        OLD.rating IS NULL AND NEW.rating IS NOT NULL
        AND (NEW.score IS NOT DISTINCT FROM OLD.score OR OLD.score IS NULL)
        AND NEW.id = OLD.id
        AND NEW.user_id = OLD.user_id
        AND NEW.problem_id = OLD.problem_id
        AND NEW.problem_version = OLD.problem_version
        AND NEW.card_epoch = OLD.card_epoch
        AND NEW.session_id IS NOT DISTINCT FROM OLD.session_id
        AND NEW.response = OLD.response
        AND NEW.auto_correct IS NOT DISTINCT FROM OLD.auto_correct
        AND NEW.latency_ms = OLD.latency_ms
        AND NEW.answered_at = OLD.answered_at
        AND NEW.client_kind = OLD.client_kind
        AND NEW.client_user_id = OLD.client_user_id
        AND NEW.client_client_id = OLD.client_client_id
    ) THEN
        RAISE EXCEPTION 'attempts are append-only';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER attempts_rating_fill_only BEFORE UPDATE ON attempts
    FOR EACH ROW EXECUTE FUNCTION attempts_rating_fill_only();
