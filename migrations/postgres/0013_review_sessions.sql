-- Review and quiz sessions. A session freezes the ordered list of problems a
-- client works through (spec: ReviewSession). The list only ever grows: when a
-- learning-step card comes due mid-session it is appended. item_count mirrors
-- the number of items and is what AppendItems compares against, so two
-- concurrent appenders cannot both extend the same list.
--
-- Whether an item is answered is not stored: it is derived from the attempts
-- that carry the session's ID (attempts.session_id, which has no foreign key),
-- so the append-only attempt log stays the only source of truth.
CREATE TABLE review_sessions (
    id           TEXT COLLATE "C" PRIMARY KEY,
    user_id      TEXT COLLATE "C" NOT NULL REFERENCES users (id),
    kind         TEXT NOT NULL CHECK (kind IN ('review', 'quiz')),
    quiz_id      TEXT COLLATE "C" REFERENCES quizzes (id),
    item_count   INTEGER NOT NULL CHECK (item_count >= 0),
    created_at   TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    CHECK ((kind = 'quiz') = (quiz_id IS NOT NULL))
);

CREATE INDEX review_sessions_user ON review_sessions (user_id, created_at);

CREATE TABLE review_session_items (
    session_id TEXT COLLATE "C" NOT NULL REFERENCES review_sessions (id),
    position   INTEGER NOT NULL CHECK (position >= 0),
    problem_id TEXT COLLATE "C" NOT NULL REFERENCES problems (id),
    card_epoch INTEGER NOT NULL CHECK (card_epoch >= 1),
    kind       TEXT NOT NULL CHECK (kind IN ('review', 'new', 'quiz')),
    PRIMARY KEY (session_id, position)
);

-- Finds the answers of one session.
CREATE INDEX attempts_user_session ON attempts (user_id, session_id);
