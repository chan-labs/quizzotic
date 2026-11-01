-- Quizzes are owned by one user and soft-deleted (attempts made in quiz
-- sessions must keep their quiz_id). quiz_items is the ordered problem list:
-- position is 0-based and contiguous, and a problem appears at most once.
-- created_by is the actor recorded as three columns.
CREATE TABLE quizzes (
    id                    TEXT PRIMARY KEY,
    owner_id              TEXT NOT NULL REFERENCES users (id),
    title                 TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL CHECK (status IN ('draft', 'live', 'archived')),
    created_by_kind       TEXT NOT NULL CHECK (created_by_kind IN ('human', 'agent', 'system')),
    created_by_user_id    TEXT NOT NULL DEFAULT '',
    created_by_client_id  TEXT NOT NULL DEFAULT '',
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    deleted_at            INTEGER
);

CREATE INDEX quizzes_owner ON quizzes (owner_id, deleted_at, id);

CREATE TABLE quiz_items (
    quiz_id     TEXT NOT NULL REFERENCES quizzes (id),
    position    INTEGER NOT NULL CHECK (position >= 0),
    problem_id  TEXT NOT NULL REFERENCES problems (id),
    PRIMARY KEY (quiz_id, position),
    UNIQUE (quiz_id, problem_id)
);

CREATE INDEX quiz_items_problem ON quiz_items (problem_id);
