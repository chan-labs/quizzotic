// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
)

// Attempts implements store.Store.
func (s *Store) Attempts() attempts.Repo { return attemptRepo{s.q} }

type attemptRepo struct{ q execer }

const attemptCols = `id, user_id, problem_id, problem_version, card_epoch, session_id, response,
	auto_correct, score, rating, latency_ms, answered_at, client_kind, client_user_id, client_client_id`

// Create appends the attempt, but only when the problem is the owner's: the
// INSERT selects nothing for another owner's problem, which is reported as
// ErrNotFound.
func (r attemptRepo) Create(ctx context.Context, ownerID domain.ID, a attempts.Attempt) error {
	if a.UserID != ownerID {
		return errors.New("attempts: attempt user does not match owner id")
	}
	var session, autoCorrect, rating, score any
	if !a.SessionID.IsNil() {
		session = a.SessionID.String()
	}
	if a.AutoCorrect != nil {
		autoCorrect = *a.AutoCorrect
	}
	if a.Rating != nil {
		rating = *a.Rating
	}
	if a.Score != nil {
		score = *a.Score
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO attempts (`+attemptCols+`)
		SELECT ?::text, ?::text, p.id, ?::integer, ?::integer, ?::text, ?::jsonb, ?::boolean, ?::double precision,
			?::integer, ?::integer, ?::timestamptz, ?::text, ?::text, ?::text
		FROM problems p WHERE p.id = ? AND p.owner_id = ?`,
		a.ID.String(), ownerID.String(), a.ProblemVersion, a.CardEpoch, session, string(a.Response),
		autoCorrect, score, rating, a.LatencyMS, ts(a.AnsweredAt), string(a.Client.Kind),
		actorUser(a.Client), a.Client.ClientID,
		a.ProblemID.String(), ownerID.String())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return attempts.ErrNotFound
	}
	return nil
}

// SetRating fills a missing rating. The WHERE clause only matches an unrated
// attempt; the table trigger enforces the same rule underneath.
func (r attemptRepo) SetRating(ctx context.Context, ownerID, id domain.ID, rating int, score *float64) error {
	var sc any
	if score != nil {
		sc = *score
	}
	res, err := r.q.ExecContext(ctx, `UPDATE attempts SET rating = ?::integer, score = COALESCE(score, ?::double precision)
		WHERE user_id = ? AND id = ? AND rating IS NULL`, rating, sc, ownerID.String(), id.String())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var one int
	err = r.q.QueryRowContext(ctx, `SELECT 1 FROM attempts WHERE user_id = ? AND id = ?`,
		ownerID.String(), id.String()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return attempts.ErrNotFound
	}
	if err != nil {
		return err
	}
	return attempts.ErrAlreadyRated
}

func (r attemptRepo) Get(ctx context.Context, ownerID, id domain.ID) (attempts.Attempt, error) {
	var (
		a                          attempts.Attempt
		aid, uid, pid, kind, actor string
		response                   []byte
		session                    sql.NullString
		autoCorrect                sql.NullBool
		rating                     sql.NullInt64
		score                      sql.NullFloat64
		answered                   time.Time
	)
	err := r.q.QueryRowContext(ctx, `SELECT `+attemptCols+` FROM attempts WHERE user_id = ? AND id = ?`,
		ownerID.String(), id.String()).Scan(&aid, &uid, &pid, &a.ProblemVersion, &a.CardEpoch, &session, &response,
		&autoCorrect, &score, &rating, &a.LatencyMS, &answered, &kind, &actor, &a.Client.ClientID)
	if errors.Is(err, sql.ErrNoRows) {
		return attempts.Attempt{}, attempts.ErrNotFound
	}
	if err != nil {
		return attempts.Attempt{}, err
	}
	if a.ID, err = domain.ParseID(aid); err != nil {
		return attempts.Attempt{}, err
	}
	if a.UserID, err = domain.ParseID(uid); err != nil {
		return attempts.Attempt{}, err
	}
	if a.ProblemID, err = domain.ParseID(pid); err != nil {
		return attempts.Attempt{}, err
	}
	if session.Valid {
		if a.SessionID, err = domain.ParseID(session.String); err != nil {
			return attempts.Attempt{}, err
		}
	}
	if a.Client.UserID, err = parseActorUser(actor); err != nil {
		return attempts.Attempt{}, err
	}
	a.Response = compactJSON(response)
	if autoCorrect.Valid {
		a.AutoCorrect = &autoCorrect.Bool
	}
	if score.Valid {
		a.Score = &score.Float64
	}
	if rating.Valid {
		n := int(rating.Int64)
		a.Rating = &n
	}
	a.AnsweredAt = answered.UTC()
	a.Client.Kind = domain.ActorKind(kind)
	return a, nil
}
