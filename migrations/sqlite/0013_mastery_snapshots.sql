-- Mastery snapshots are the daily rollup behind the over-time view: one row per
-- user, active tag and study day, written by the in-process scheduler at the
-- user's rollover hour. date is the study day (YYYY-MM-DD in the user's zone);
-- (user_id, tag_id, date) is the key, so writing a day twice is a no-op.
-- Snapshots are history, not a pure projection of current state, but
-- `quizzotic admin rebuild --history` can regenerate them from the attempt log.
CREATE TABLE mastery_snapshots (
    user_id    TEXT NOT NULL REFERENCES users (id),
    tag_id     TEXT NOT NULL REFERENCES tags (id),
    date       TEXT NOT NULL CHECK (length(date) = 10),
    recall     REAL NOT NULL CHECK (recall >= 0),
    cards_seen INTEGER NOT NULL CHECK (cards_seen >= 0),
    PRIMARY KEY (user_id, tag_id, date)
);
