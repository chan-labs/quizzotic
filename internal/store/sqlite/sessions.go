// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/review"
)

// Sessions implements store.Store.
func (s *Store) Sessions() review.SessionRepo { return sessionRepo{s.q} }

type sessionRepo struct{ q execer }

// Create stores the session and its items. The session row, the quiz and every
// item's problem are checked against the owner: the INSERTs select nothing for
// another owner's rows, which is reported as ErrNotFound.
func (r sessionRepo) Create(ctx context.Context, ownerID domain.ID, s review.Session) error {
	if s.UserID != ownerID {
		return errors.New("review: session user does not match owner id")
	}
	var quiz any
	if s.Kind == review.SessionQuiz {
		quiz = s.QuizID.String()
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO review_sessions (id, user_id, kind, quiz_id, item_count, created_at)
		SELECT ?, ?, ?, ?, ?, ?
		WHERE ? IS NULL OR EXISTS (SELECT 1 FROM quizzes WHERE id = ? AND owner_id = ?)`,
		s.ID.String(), ownerID.String(), string(s.Kind), quiz, len(s.Items), Millis(s.CreatedAt),
		quiz, quiz, ownerID.String())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return review.ErrNotFound
	}
	return r.insertItems(ctx, ownerID, s.ID, 0, s.Items)
}

func (r sessionRepo) insertItems(ctx context.Context, ownerID, sessionID domain.ID, from int, items []review.Item) error {
	for i, it := range items {
		res, err := r.q.ExecContext(ctx, `INSERT INTO review_session_items (session_id, position, problem_id, card_epoch, kind)
			SELECT ?, ?, p.id, ?, ? FROM problems p WHERE p.id = ? AND p.owner_id = ?`,
			sessionID.String(), from+i, it.Epoch, string(it.Kind), it.ProblemID.String(), ownerID.String())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return review.ErrNotFound
		}
	}
	return nil
}

func (r sessionRepo) Get(ctx context.Context, ownerID, id domain.ID) (review.Session, error) {
	var (
		s         review.Session
		kind      string
		quiz      sql.NullString
		created   int64
		completed sql.NullInt64
	)
	err := r.q.QueryRowContext(ctx, `SELECT kind, quiz_id, created_at, completed_at FROM review_sessions
		WHERE user_id = ? AND id = ?`, ownerID.String(), id.String()).Scan(&kind, &quiz, &created, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return review.Session{}, review.ErrSessionNotFound
	}
	if err != nil {
		return review.Session{}, err
	}
	s.ID, s.UserID, s.Kind, s.CreatedAt = id, ownerID, review.SessionKind(kind), FromMillis(created)
	if quiz.Valid {
		if s.QuizID, err = domain.ParseID(quiz.String); err != nil {
			return review.Session{}, err
		}
	}
	if completed.Valid {
		t := FromMillis(completed.Int64)
		s.CompletedAt = &t
	}
	rows, err := r.q.QueryContext(ctx, `SELECT problem_id, card_epoch, kind FROM review_session_items
		WHERE session_id = ? ORDER BY position`, id.String())
	if err != nil {
		return review.Session{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, ik string
		var it review.Item
		if err := rows.Scan(&pid, &it.Epoch, &ik); err != nil {
			return review.Session{}, err
		}
		if it.ProblemID, err = domain.ParseID(pid); err != nil {
			return review.Session{}, err
		}
		it.Kind = review.ItemKind(ik)
		s.Items = append(s.Items, it)
	}
	return s, rows.Err()
}

// AppendItems bumps item_count only when it still equals have; that guarded
// UPDATE is what serializes concurrent appenders.
func (r sessionRepo) AppendItems(ctx context.Context, ownerID, id domain.ID, have int, items []review.Item) error {
	res, err := r.q.ExecContext(ctx, `UPDATE review_sessions SET item_count = ? WHERE user_id = ? AND id = ? AND item_count = ?`,
		have+len(items), ownerID.String(), id.String(), have)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var one int
		err := r.q.QueryRowContext(ctx, `SELECT 1 FROM review_sessions WHERE user_id = ? AND id = ?`,
			ownerID.String(), id.String()).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return review.ErrSessionNotFound
		}
		if err != nil {
			return err
		}
		return review.ErrSessionChanged
	}
	return r.insertItems(ctx, ownerID, id, have, items)
}

func (r sessionRepo) Complete(ctx context.Context, ownerID, id domain.ID, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `UPDATE review_sessions SET completed_at = COALESCE(completed_at, ?)
		WHERE user_id = ? AND id = ?`, Millis(at), ownerID.String(), id.String())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return review.ErrSessionNotFound
	}
	return nil
}
