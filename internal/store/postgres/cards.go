// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/review"
)

// Cards implements store.Store.
func (s *Store) Cards() review.Repo { return cardRepo{s.q} }

type cardRepo struct{ q execer }

const cardCols = `user_id, problem_id, card_epoch, state, step, stability, difficulty, due, last_review, reps, lapses`

// Put upserts the card, but only for the owner's problem: the INSERT selects
// nothing for another owner's problem, which is reported as ErrNotFound.
func (r cardRepo) Put(ctx context.Context, ownerID domain.ID, c review.Card) error {
	if c.UserID != ownerID {
		return errors.New("review: card user does not match owner id")
	}
	state := string(c.State)
	if state == "" {
		state = string(fsrs.New)
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO cards (`+cardCols+`)
		SELECT ?::text, p.id, ?::integer, ?::text, ?::integer, ?::double precision, ?::double precision,
			?::timestamptz, ?::timestamptz, ?::integer, ?::integer
		FROM problems p WHERE p.id = ? AND p.owner_id = ?
		ON CONFLICT (user_id, problem_id) DO UPDATE SET
			card_epoch = excluded.card_epoch, state = excluded.state, step = excluded.step,
			stability = excluded.stability, difficulty = excluded.difficulty, due = excluded.due,
			last_review = excluded.last_review, reps = excluded.reps, lapses = excluded.lapses`,
		ownerID.String(), c.Epoch, state, c.Step, c.Stability, c.Difficulty, ts(c.Due), nullTS(c.LastReview),
		c.Reps, c.Lapses, c.ProblemID.String(), ownerID.String())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return review.ErrNotFound
	}
	return nil
}

func (r cardRepo) Get(ctx context.Context, ownerID, problemID domain.ID) (review.Card, error) {
	c, err := scanCard(r.q.QueryRowContext(ctx, `SELECT `+cardCols+` FROM cards WHERE user_id = ? AND problem_id = ?`,
		ownerID.String(), problemID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return review.Card{}, review.ErrNotFound
	}
	return c, err
}

func (r cardRepo) Delete(ctx context.Context, ownerID, problemID domain.ID) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM cards WHERE user_id = ? AND problem_id = ?`,
		ownerID.String(), problemID.String())
	return err
}

func (r cardRepo) DeleteAll(ctx context.Context, ownerID domain.ID) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM cards WHERE user_id = ?`, ownerID.String())
	return err
}

func (r cardRepo) List(ctx context.Context, ownerID domain.ID) ([]review.Card, error) {
	return r.Query(ctx, ownerID, review.CardFilter{})
}

func (r cardRepo) Query(ctx context.Context, ownerID domain.ID, f review.CardFilter) ([]review.Card, error) {
	q := `SELECT ` + cardCols + ` FROM cards WHERE user_id = ?`
	args := []any{ownerID.String()}
	if !f.DueBefore.IsZero() {
		q += ` AND due < ?`
		args = append(args, ts(f.DueBefore))
	}
	if !f.TagID.IsNil() {
		q += ` AND problem_id IN (SELECT pt.problem_id FROM problem_tags pt WHERE pt.tag_id = ?)`
		args = append(args, f.TagID.String())
	}
	if !f.After.IsNil() {
		q += ` AND problem_id > ?`
		args = append(args, f.After.String())
	}
	q += ` ORDER BY problem_id`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := r.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []review.Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanCard(row rowScanner) (review.Card, error) {
	var (
		c        review.Card
		uid, pid string
		state    string
		last     sql.NullTime
	)
	if err := row.Scan(&uid, &pid, &c.Epoch, &state, &c.Step, &c.Stability, &c.Difficulty, &c.Due, &last, &c.Reps, &c.Lapses); err != nil {
		return review.Card{}, err
	}
	var err error
	if c.UserID, err = domain.ParseID(uid); err != nil {
		return review.Card{}, err
	}
	if c.ProblemID, err = domain.ParseID(pid); err != nil {
		return review.Card{}, err
	}
	c.State = fsrs.State(state)
	c.Due = c.Due.UTC()
	if last.Valid {
		c.LastReview = last.Time.UTC()
	}
	return c, nil
}
