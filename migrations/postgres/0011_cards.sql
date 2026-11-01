-- Cards are a projection of the attempt log: one user's FSRS scheduling state
-- for one problem. Nothing here is a source of truth; the table can be dropped
-- and rebuilt from attempts at any time (quizzotic admin rebuild).
--
-- A user has at most one card per problem, at the problem's current card
-- epoch. A problem without a scheduled attempt at its current epoch has no
-- row: that is a new card, created lazily by its first answer. last_review is
-- NULL until the first review.
CREATE TABLE cards (
    user_id       TEXT COLLATE "C" NOT NULL REFERENCES users (id),
    problem_id    TEXT COLLATE "C" NOT NULL REFERENCES problems (id),
    card_epoch    INTEGER NOT NULL CHECK (card_epoch >= 1),
    state         TEXT NOT NULL CHECK (state IN ('new', 'learning', 'review', 'relearning')),
    step          INTEGER NOT NULL CHECK (step >= 0),
    stability     DOUBLE PRECISION NOT NULL CHECK (stability >= 0),
    difficulty    DOUBLE PRECISION NOT NULL CHECK (difficulty >= 0),
    due           TIMESTAMPTZ NOT NULL,
    last_review   TIMESTAMPTZ,
    reps          INTEGER NOT NULL CHECK (reps >= 0),
    lapses        INTEGER NOT NULL CHECK (lapses >= 0),
    PRIMARY KEY (user_id, problem_id)
);

CREATE INDEX cards_user_due ON cards (user_id, due);
