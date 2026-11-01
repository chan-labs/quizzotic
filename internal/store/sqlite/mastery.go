// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/review"
)

// Mastery implements store.Store.
func (s *Store) Mastery() metrics.Repo { return masteryRepo{s.q} }

type masteryRepo struct{ q execer }

const masteryCols = `user_id, tag_id, recall, stability_median, cards_total, cards_seen, reviews, lapses, last_attempt_at`

// Put upserts the row, but only for the owner's tag: the INSERT selects
// nothing for another owner's tag, which is reported as ErrNotFound.
func (r masteryRepo) Put(ctx context.Context, ownerID domain.ID, m metrics.TagMastery) error {
	if m.UserID != ownerID {
		return errors.New("metrics: mastery user does not match owner id")
	}
	var last any
	if !m.LastAttemptAt.IsZero() {
		last = Millis(m.LastAttemptAt)
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO tag_mastery (`+masteryCols+`)
		SELECT ?, t.id, ?, ?, ?, ?, ?, ?, ?
		FROM tags t WHERE t.id = ? AND t.owner_id = ?
		ON CONFLICT (user_id, tag_id) DO UPDATE SET
			recall = excluded.recall, stability_median = excluded.stability_median,
			cards_total = excluded.cards_total, cards_seen = excluded.cards_seen,
			reviews = excluded.reviews, lapses = excluded.lapses, last_attempt_at = excluded.last_attempt_at`,
		ownerID.String(), m.Recall, m.StabilityMedian, m.CardsTotal, m.CardsSeen, m.Reviews, m.Lapses, last,
		m.TagID.String(), ownerID.String())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return metrics.ErrNotFound
	}
	return nil
}

func (r masteryRepo) Get(ctx context.Context, ownerID, tagID domain.ID) (metrics.TagMastery, error) {
	m, err := scanMastery(r.q.QueryRowContext(ctx, `SELECT `+masteryCols+` FROM tag_mastery WHERE user_id = ? AND tag_id = ?`,
		ownerID.String(), tagID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return metrics.TagMastery{}, metrics.ErrNotFound
	}
	return m, err
}

func (r masteryRepo) Delete(ctx context.Context, ownerID, tagID domain.ID) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM tag_mastery WHERE user_id = ? AND tag_id = ?`,
		ownerID.String(), tagID.String())
	return err
}

func (r masteryRepo) DeleteAll(ctx context.Context, ownerID domain.ID) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM tag_mastery WHERE user_id = ?`, ownerID.String())
	return err
}

func (r masteryRepo) List(ctx context.Context, ownerID domain.ID) ([]metrics.TagMastery, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+masteryCols+` FROM tag_mastery WHERE user_id = ? ORDER BY tag_id`, ownerID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metrics.TagMastery
	for rows.Next() {
		m, err := scanMastery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func scanMastery(row rowScanner) (metrics.TagMastery, error) {
	var (
		m        metrics.TagMastery
		uid, tid string
		last     sql.NullInt64
	)
	if err := row.Scan(&uid, &tid, &m.Recall, &m.StabilityMedian, &m.CardsTotal, &m.CardsSeen, &m.Reviews, &m.Lapses, &last); err != nil {
		return metrics.TagMastery{}, err
	}
	var err error
	if m.UserID, err = domain.ParseID(uid); err != nil {
		return metrics.TagMastery{}, err
	}
	if m.TagID, err = domain.ParseID(tid); err != nil {
		return metrics.TagMastery{}, err
	}
	if last.Valid {
		m.LastAttemptAt = FromMillis(last.Int64)
	}
	return m, nil
}

func (r masteryRepo) idList(ctx context.Context, query string, args ...any) ([]domain.ID, error) {
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ID
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		id, err := domain.ParseID(s)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r masteryRepo) ActiveTagIDs(ctx context.Context, ownerID domain.ID) ([]domain.ID, error) {
	return r.idList(ctx, `SELECT id FROM tags WHERE owner_id = ? AND status = 'active' ORDER BY id`, ownerID.String())
}

func (r masteryRepo) ProblemTagIDs(ctx context.Context, ownerID, problemID domain.ID) ([]domain.ID, error) {
	return r.idList(ctx, `SELECT pt.tag_id FROM problem_tags pt
		JOIN problems p ON p.id = pt.problem_id AND p.owner_id = ?
		JOIN tags t ON t.id = pt.tag_id AND t.owner_id = ?
		WHERE pt.problem_id = ? ORDER BY pt.tag_id`,
		ownerID.String(), ownerID.String(), problemID.String())
}

// inClause returns " AND col IN (?, ...)" and its arguments, or nothing for an
// empty list.
func inClause(col string, ids []domain.ID) (string, []any) {
	if len(ids) == 0 {
		return "", nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id.String()
	}
	return " AND " + col + " IN (?" + strings.Repeat(", ?", len(ids)-1) + ")", args
}

func (r masteryRepo) TagCards(ctx context.Context, ownerID domain.ID, tagIDs []domain.ID) ([]metrics.TagCard, error) {
	in, inArgs := inClause("t.id", tagIDs)
	args := append([]any{ownerID.String(), ownerID.String(), ownerID.String()}, inArgs...)
	rows, err := r.q.QueryContext(ctx, `SELECT t.id, p.id, c.card_epoch, c.state, c.step, c.stability, c.difficulty,
			c.due, c.last_review, c.reps, c.lapses
		FROM tags t
		JOIN problem_tags pt ON pt.tag_id = t.id
		JOIN problems p ON p.id = pt.problem_id AND p.owner_id = ? AND p.status = 'live' AND p.deleted_at IS NULL
		LEFT JOIN cards c ON c.problem_id = p.id AND c.user_id = ?
		WHERE t.owner_id = ? AND t.status = 'active'`+in+`
		ORDER BY t.id, p.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metrics.TagCard
	for rows.Next() {
		var (
			tid, pid string
			epoch    sql.NullInt64
			state    sql.NullString
			step     sql.NullInt64
			stab     sql.NullFloat64
			diff     sql.NullFloat64
			due      sql.NullInt64
			last     sql.NullInt64
			reps     sql.NullInt64
			lapses   sql.NullInt64
		)
		if err := rows.Scan(&tid, &pid, &epoch, &state, &step, &stab, &diff, &due, &last, &reps, &lapses); err != nil {
			return nil, err
		}
		var tc metrics.TagCard
		if tc.TagID, err = domain.ParseID(tid); err != nil {
			return nil, err
		}
		if tc.ProblemID, err = domain.ParseID(pid); err != nil {
			return nil, err
		}
		if epoch.Valid {
			c := review.Card{UserID: ownerID, ProblemID: tc.ProblemID, Epoch: int(epoch.Int64),
				Reps: int(reps.Int64), Lapses: int(lapses.Int64)}
			c.State = fsrs.State(state.String)
			c.Step = int(step.Int64)
			c.Stability, c.Difficulty = stab.Float64, diff.Float64
			c.Due = FromMillis(due.Int64)
			if last.Valid {
				c.LastReview = FromMillis(last.Int64)
			}
			tc.Card = &c
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

func (r masteryRepo) LastAttempts(ctx context.Context, ownerID domain.ID, tagIDs []domain.ID) (map[domain.ID]time.Time, error) {
	in, inArgs := inClause("pt.tag_id", tagIDs)
	args := append([]any{ownerID.String(), ownerID.String(), ownerID.String()}, inArgs...)
	rows, err := r.q.QueryContext(ctx, `SELECT pt.tag_id, MAX(a.answered_at)
		FROM problem_tags pt
		JOIN problems p ON p.id = pt.problem_id AND p.owner_id = ? AND p.deleted_at IS NULL
		JOIN attempts a ON a.problem_id = p.id AND a.user_id = ?
		JOIN tags t ON t.id = pt.tag_id AND t.owner_id = ?
		WHERE 1 = 1`+in+`
		GROUP BY pt.tag_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.ID]time.Time{}
	for rows.Next() {
		var (
			tid string
			ms  int64
		)
		if err := rows.Scan(&tid, &ms); err != nil {
			return nil, err
		}
		id, err := domain.ParseID(tid)
		if err != nil {
			return nil, err
		}
		out[id] = FromMillis(ms)
	}
	return out, rows.Err()
}
