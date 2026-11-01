// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func (r tagOpRepo) MarkReverted(ctx context.Context, ownerID, id domain.ID, by domain.Actor, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `UPDATE tag_operations SET reverted_at = ?,
		reverted_by_kind = ?, reverted_by_user_id = ?, reverted_by_client_id = ?
		WHERE owner_id = ? AND id = ? AND status = 'applied' AND reverted_at IS NULL`,
		ts(at), string(by.Kind), actorUser(by), by.ClientID, ownerID.String(), id.String())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	op, err := r.Get(ctx, ownerID, id)
	if err != nil {
		return err
	}
	if op.Status != tags.OpApplied {
		return tags.ErrOpNotApplied
	}
	return tags.ErrOpReverted
}

func (r tagOpRepo) Restore(ctx context.Context, ownerID domain.ID, snap tags.Snapshot, removeTagIDs []domain.ID) error {
	o := ownerID.String()
	var all []domain.ID
	for _, t := range snap.Tags {
		all = append(all, t.ID)
	}
	all = append(all, removeTagIDs...)
	for _, id := range all {
		var n int
		if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM tags WHERE id = ? AND owner_id = ?`, id.String(), o).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return tags.ErrNotFound
		}
	}
	if len(all) == 0 {
		return nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(all)), ",")
	ids := make([]any, len(all))
	for i, id := range all {
		ids[i] = id.String()
	}
	exec := func(q string, args ...any) error {
		_, err := r.q.ExecContext(ctx, q, args...)
		return err
	}
	// Clear what the operation (and anything after it) put on these tags,
	// then write back the snapshot.
	if err := exec(`DELETE FROM problem_tags WHERE tag_id IN (`+marks+`)`, ids...); err != nil {
		return err
	}
	if err := exec(`DELETE FROM tag_aliases WHERE owner_id = ? AND tag_id IN (`+marks+`)`, append([]any{o}, ids...)...); err != nil {
		return err
	}
	for _, id := range removeTagIDs {
		if slices.ContainsFunc(snap.Tags, func(t tags.SnapshotTag) bool { return t.ID == id }) {
			continue
		}
		if err := exec(`DELETE FROM tags WHERE owner_id = ? AND id = ?`, o, id.String()); err != nil {
			return err
		}
	}
	for _, t := range snap.Tags {
		var merged any
		if !t.MergedInto.IsNil() {
			merged = t.MergedInto.String()
		}
		err := exec(`UPDATE tags SET slug = ?, name = ?, description = ?, status = ?, merged_into = ?,
			created_at = ?, updated_at = ? WHERE owner_id = ? AND id = ?`,
			t.Slug, t.Name, t.Description, string(t.Status), merged, ts(t.CreatedAt), ts(t.UpdatedAt), o, t.ID.String())
		if isUnique(err) {
			return tags.ErrSlugTaken
		}
		if err != nil {
			return err
		}
	}
	for _, t := range snap.Tags {
		for _, a := range t.Aliases {
			err := exec(`INSERT INTO tag_aliases (owner_id, slug, tag_id) VALUES (?, ?, ?)`, o, a, t.ID.String())
			if isUnique(err) {
				return tags.ErrSlugTaken
			}
			if err != nil {
				return err
			}
		}
	}
	for _, m := range snap.Mappings {
		if err := exec(`INSERT INTO problem_tags (problem_id, tag_id) VALUES (?, ?)`, m.ProblemID.String(), m.TagID.String()); err != nil {
			return err
		}
	}
	return nil
}
