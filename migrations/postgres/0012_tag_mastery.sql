-- Tag mastery is a projection of the card projection: one row per user and
-- active tag, holding the tag's recall, coverage inputs, median stability and
-- lapse inputs as of the last time the row was computed. Nothing here is a
-- source of truth; the table can be dropped and rebuilt at any time
-- (quizzotic admin rebuild).
--
-- cards_total counts the tag's live problems and cards_seen those with a
-- reviewed card, so coverage is cards_seen / cards_total. reviews and lapses
-- are totals over the studied cards (lapse rate = lapses / reviews). recall is
-- 0 when cards_seen is 0. last_attempt_at is NULL until the tag has an attempt.
CREATE TABLE tag_mastery (
    user_id          TEXT COLLATE "C" NOT NULL REFERENCES users (id),
    tag_id           TEXT COLLATE "C" NOT NULL REFERENCES tags (id),
    recall           DOUBLE PRECISION NOT NULL CHECK (recall >= 0),
    stability_median DOUBLE PRECISION NOT NULL CHECK (stability_median >= 0),
    cards_total      INTEGER NOT NULL CHECK (cards_total >= 0),
    cards_seen       INTEGER NOT NULL CHECK (cards_seen >= 0 AND cards_seen <= cards_total),
    reviews          INTEGER NOT NULL CHECK (reviews >= 0),
    lapses           INTEGER NOT NULL CHECK (lapses >= 0),
    last_attempt_at  TIMESTAMPTZ,
    PRIMARY KEY (user_id, tag_id)
);
