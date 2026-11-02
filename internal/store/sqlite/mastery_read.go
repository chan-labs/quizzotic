// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/review"
)

// TagLabels implements metrics.Repo.
func (r masteryRepo) TagLabels(ctx context.Context, ownerID domain.ID) ([]metrics.TagLabel, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, slug, name FROM tags
		WHERE owner_id = ? AND status = 'active' ORDER BY id`, ownerID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metrics.TagLabel
	for rows.Next() {
		var id string
		var l metrics.TagLabel
		if err := rows.Scan(&id, &l.Slug, &l.Name); err != nil {
			return nil, err
		}
		if l.ID, err = domain.ParseID(id); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// StudiedCards implements metrics.Repo.
func (r masteryRepo) StudiedCards(ctx context.Context, ownerID domain.ID) ([]metrics.ProblemCard, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT p.id, v.type, v.prompt, c.card_epoch, c.state, c.step, c.stability,
			c.difficulty, c.due, c.last_review, c.reps, c.lapses
		FROM cards c
		JOIN problems p ON p.id = c.problem_id AND p.owner_id = ? AND p.status = 'live' AND p.deleted_at IS NULL
		JOIN problem_versions v ON v.problem_id = p.id AND v.version = p.current_version
		WHERE c.user_id = ? AND c.reps > 0 AND c.last_review IS NOT NULL
		ORDER BY p.id`, ownerID.String(), ownerID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metrics.ProblemCard
	for rows.Next() {
		var (
			pid, state string
			epoch      int
			pc         metrics.ProblemCard
			due, last  int64
		)
		c := review.Card{UserID: ownerID}
		if err := rows.Scan(&pid, &pc.Type, &pc.Prompt, &epoch, &state, &c.Step, &c.Stability, &c.Difficulty,
			&due, &last, &c.Reps, &c.Lapses); err != nil {
			return nil, err
		}
		if pc.ProblemID, err = domain.ParseID(pid); err != nil {
			return nil, err
		}
		c.ProblemID, c.Epoch, c.State = pc.ProblemID, epoch, fsrs.State(state)
		c.Due, c.LastReview = FromMillis(due), FromMillis(last)
		pc.Card = c
		out = append(out, pc)
	}
	return out, rows.Err()
}
