// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func (r tagRepo) Merge(ctx context.Context, ownerID domain.ID, sourceIDs []domain.ID, targetID domain.ID, now time.Time) error {
	for _, id := range append(append([]domain.ID{}, sourceIDs...), targetID) {
		var n int
		if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM tags WHERE id = ? AND owner_id = ?`,
			id.String(), ownerID.String()).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return tags.ErrNotFound
		}
	}
	marks := make([]string, len(sourceIDs))
	src := make([]any, len(sourceIDs))
	for i, id := range sourceIDs {
		marks[i] = "?"
		src[i] = id.String()
	}
	in := `(` + strings.Join(marks, ",") + `)`
	tid, o := targetID.String(), ownerID.String()
	exec := func(q string, lead []any) error {
		_, err := r.q.ExecContext(ctx, q, append(lead, src...)...)
		return err
	}
	if err := exec(`INSERT OR IGNORE INTO problem_tags (problem_id, tag_id)
		SELECT problem_id, ? FROM problem_tags WHERE tag_id IN `+in, []any{tid}); err != nil {
		return err
	}
	if err := exec(`DELETE FROM problem_tags WHERE tag_id IN `+in, nil); err != nil {
		return err
	}
	// Source slugs become aliases of the target; existing source aliases are
	// repointed. Both happen while the tags still hold their slugs.
	if err := exec(`INSERT INTO tag_aliases (owner_id, slug, tag_id)
		SELECT owner_id, slug, ? FROM tags WHERE owner_id = ? AND id IN `+in, []any{tid, o}); err != nil {
		return err
	}
	if err := exec(`UPDATE tag_aliases SET tag_id = ? WHERE owner_id = ? AND tag_id IN `+in, []any{tid, o}); err != nil {
		return err
	}
	if err := exec(`UPDATE tags SET status = 'merged', merged_into = ?, updated_at = ?
		WHERE owner_id = ? AND id IN `+in, []any{tid, Millis(now), o}); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `UPDATE tags SET updated_at = ? WHERE id = ? AND owner_id = ?`, Millis(now), tid, o)
	return err
}
