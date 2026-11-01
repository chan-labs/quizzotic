// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"

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
		autoCorrect = boolInt(*a.AutoCorrect)
	}
	if a.Rating != nil {
		rating = *a.Rating
	}
	if a.Score != nil {
		score = *a.Score
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO attempts (`+attemptCols+`)
		SELECT ?, ?, p.id, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		FROM problems p WHERE p.id = ? AND p.owner_id = ?`,
		a.ID.String(), ownerID.String(), a.ProblemVersion, a.CardEpoch, session, string(a.Response),
		autoCorrect, score, rating, a.LatencyMS, Millis(a.AnsweredAt), string(a.Client.Kind),
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
	res, err := r.q.ExecContext(ctx, `UPDATE attempts SET rating = ?, score = COALESCE(score, ?)
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

// rowScanner is *sql.Row or *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanAttempt(row rowScanner) (attempts.Attempt, error) {
	var (
		a                          attempts.Attempt
		aid, uid, pid, kind, actor string
		response                   string
		session                    sql.NullString
		autoCorrect, rating        sql.NullInt64
		score                      sql.NullFloat64
		answered                   int64
	)
	err := row.Scan(&aid, &uid, &pid, &a.ProblemVersion, &a.CardEpoch, &session, &response,
		&autoCorrect, &score, &rating, &a.LatencyMS, &answered, &kind, &actor, &a.Client.ClientID)
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
	a.Response = []byte(response)
	if autoCorrect.Valid {
		b := autoCorrect.Int64 == 1
		a.AutoCorrect = &b
	}
	if score.Valid {
		a.Score = &score.Float64
	}
	if rating.Valid {
		n := int(rating.Int64)
		a.Rating = &n
	}
	a.AnsweredAt = FromMillis(answered)
	a.Client.Kind = domain.ActorKind(kind)
	return a, nil
}

func (r attemptRepo) Get(ctx context.Context, ownerID, id domain.ID) (attempts.Attempt, error) {
	a, err := scanAttempt(r.q.QueryRowContext(ctx, `SELECT `+attemptCols+` FROM attempts WHERE user_id = ? AND id = ?`,
		ownerID.String(), id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return attempts.Attempt{}, attempts.ErrNotFound
	}
	return a, err
}

// List returns the owner's attempts ordered by ID (oldest first).
func (r attemptRepo) List(ctx context.Context, ownerID domain.ID, f attempts.ListFilter) ([]attempts.Attempt, error) {
	q := `SELECT ` + attemptCols + ` FROM attempts WHERE user_id = ?`
	args := []any{ownerID.String()}
	if !f.ProblemID.IsNil() {
		q += ` AND problem_id = ?`
		args = append(args, f.ProblemID.String())
	}
	if !f.TagID.IsNil() {
		q += ` AND problem_id IN (SELECT pt.problem_id FROM problem_tags pt WHERE pt.tag_id = ?)`
		args = append(args, f.TagID.String())
	}
	if !f.SessionID.IsNil() {
		q += ` AND session_id = ?`
		args = append(args, f.SessionID.String())
	}
	if !f.Since.IsZero() {
		q += ` AND answered_at >= ?`
		args = append(args, Millis(f.Since))
	}
	if !f.Until.IsZero() {
		q += ` AND answered_at < ?`
		args = append(args, Millis(f.Until))
	}
	if !f.After.IsNil() {
		q += ` AND id > ?`
		args = append(args, f.After.String())
	}
	q += ` ORDER BY id`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := r.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []attempts.Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ForCard lists the owner's attempts at one problem and card epoch, oldest
// answer first (ties broken by ID, which is time-ordered).
func (r attemptRepo) ForCard(ctx context.Context, ownerID, problemID domain.ID, epoch int) ([]attempts.Attempt, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+attemptCols+` FROM attempts
		WHERE user_id = ? AND problem_id = ? AND card_epoch = ? ORDER BY answered_at, id`,
		ownerID.String(), problemID.String(), epoch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []attempts.Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
