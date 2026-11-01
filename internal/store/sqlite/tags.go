// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// Tags implements store.Store.
func (s *Store) Tags() tags.Repo { return tagRepo{s.q} }

type tagRepo struct{ q execer }

const tagCols = `id, owner_id, slug, name, description, status, merged_into, created_at, updated_at`

func scanTag(r scanner) (tags.Tag, error) {
	var (
		t                 tags.Tag
		id, owner, status string
		merged            sql.NullString
		created, updated  int64
	)
	if err := r.Scan(&id, &owner, &t.Slug, &t.Name, &t.Description, &status, &merged, &created, &updated); err != nil {
		return tags.Tag{}, err
	}
	var err error
	if t.ID, err = domain.ParseID(id); err != nil {
		return tags.Tag{}, err
	}
	if t.OwnerID, err = domain.ParseID(owner); err != nil {
		return tags.Tag{}, err
	}
	if merged.Valid {
		if t.MergedInto, err = domain.ParseID(merged.String); err != nil {
			return tags.Tag{}, err
		}
	}
	t.Status = tags.Status(status)
	t.CreatedAt, t.UpdatedAt = FromMillis(created), FromMillis(updated)
	t.Aliases = []string{}
	return t, nil
}

// withAliases fills in Aliases for every tag in ts with one query.
func (r tagRepo) withAliases(ctx context.Context, ownerID domain.ID, ts []tags.Tag) error {
	if len(ts) == 0 {
		return nil
	}
	idx := make(map[string]int, len(ts))
	args := []any{ownerID.String()}
	marks := make([]string, len(ts))
	for i, t := range ts {
		idx[t.ID.String()] = i
		args = append(args, t.ID.String())
		marks[i] = "?"
	}
	rows, err := r.q.QueryContext(ctx, `SELECT tag_id, slug FROM tag_aliases
		WHERE owner_id = ? AND tag_id IN (`+strings.Join(marks, ",")+`) ORDER BY slug`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var tid, slug string
		if err := rows.Scan(&tid, &slug); err != nil {
			return err
		}
		i := idx[tid]
		ts[i].Aliases = append(ts[i].Aliases, slug)
	}
	return rows.Err()
}

func (r tagRepo) one(ctx context.Context, ownerID domain.ID, where string, args ...any) (tags.Tag, error) {
	t, err := scanTag(r.q.QueryRowContext(ctx, `SELECT `+tagCols+` FROM tags WHERE owner_id = ? AND `+where,
		append([]any{ownerID.String()}, args...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return tags.Tag{}, tags.ErrNotFound
	}
	if err != nil {
		return tags.Tag{}, err
	}
	ts := []tags.Tag{t}
	if err := r.withAliases(ctx, ownerID, ts); err != nil {
		return tags.Tag{}, err
	}
	return ts[0], nil
}

// slugInUse reports whether slug is an active slug or an alias of the owner,
// ignoring the tag exceptID (use domain.Nil to ignore none).
func (r tagRepo) slugInUse(ctx context.Context, ownerID domain.ID, slug string, exceptID domain.ID) (bool, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM tags WHERE owner_id = ?1 AND slug = ?2 AND status = 'active' AND id <> ?3) +
		(SELECT count(*) FROM tag_aliases WHERE owner_id = ?1 AND slug = ?2 AND tag_id <> ?3)`,
		ownerID.String(), slug, exceptID.String()).Scan(&n)
	return n > 0, err
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (r tagRepo) Create(ctx context.Context, ownerID domain.ID, t tags.Tag) error {
	if t.OwnerID != ownerID {
		return errors.New("tags: tag owner does not match owner id")
	}
	if used, err := r.slugInUse(ctx, ownerID, t.Slug, domain.Nil); err != nil {
		return err
	} else if used {
		return tags.ErrSlugTaken
	}
	var merged any
	if !t.MergedInto.IsNil() {
		merged = t.MergedInto.String()
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO tags (`+tagCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID.String(), ownerID.String(), t.Slug, t.Name, t.Description, string(t.Status), merged,
		Millis(t.CreatedAt), Millis(t.UpdatedAt))
	if isUnique(err) {
		return tags.ErrSlugTaken
	}
	return err
}

func (r tagRepo) Get(ctx context.Context, ownerID, id domain.ID) (tags.Tag, error) {
	return r.one(ctx, ownerID, `id = ?`, id.String())
}

func (r tagRepo) GetBySlug(ctx context.Context, ownerID domain.ID, slug string) (tags.Tag, error) {
	return r.one(ctx, ownerID, `slug = ? AND status = 'active'`, slug)
}

func (r tagRepo) GetByAlias(ctx context.Context, ownerID domain.ID, slug string) (tags.Tag, error) {
	return r.one(ctx, ownerID, `id = (SELECT tag_id FROM tag_aliases WHERE owner_id = tags.owner_id AND slug = ?)`, slug)
}

func (r tagRepo) List(ctx context.Context, ownerID domain.ID, f tags.ListFilter) ([]tags.Tag, error) {
	q := `SELECT ` + tagCols + ` FROM tags WHERE owner_id = ? AND status = ?`
	args := []any{ownerID.String(), string(f.Status)}
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
	var out []tags.Tag
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, r.withAliases(ctx, ownerID, out)
}

func (r tagRepo) Update(ctx context.Context, ownerID domain.ID, t tags.Tag) error {
	var old string
	err := r.q.QueryRowContext(ctx, `SELECT slug FROM tags WHERE id = ? AND owner_id = ?`,
		t.ID.String(), ownerID.String()).Scan(&old)
	if errors.Is(err, sql.ErrNoRows) {
		return tags.ErrNotFound
	}
	if err != nil {
		return err
	}
	if t.Slug != old {
		if used, err := r.slugInUse(ctx, ownerID, t.Slug, t.ID); err != nil {
			return err
		} else if used {
			return tags.ErrSlugTaken
		}
		// Own alias equal to the new slug: the tag takes its old name back.
		if _, err := r.q.ExecContext(ctx, `DELETE FROM tag_aliases WHERE owner_id = ? AND slug = ?`,
			ownerID.String(), t.Slug); err != nil {
			return err
		}
	}
	_, err = r.q.ExecContext(ctx, `UPDATE tags SET slug = ?, name = ?, description = ?, updated_at = ?
		WHERE id = ? AND owner_id = ?`,
		t.Slug, t.Name, t.Description, Millis(t.UpdatedAt), t.ID.String(), ownerID.String())
	if isUnique(err) {
		return tags.ErrSlugTaken
	}
	if err != nil {
		return err
	}
	if t.Slug != old {
		if _, err := r.q.ExecContext(ctx, `INSERT INTO tag_aliases (owner_id, slug, tag_id) VALUES (?, ?, ?)`,
			ownerID.String(), old, t.ID.String()); err != nil {
			return err
		}
	}
	return nil
}

func (r tagRepo) ProblemIDs(ctx context.Context, ownerID, tagID, after domain.ID, limit int) ([]domain.ID, error) {
	q := `SELECT pt.problem_id FROM problem_tags pt
		JOIN problems p ON p.id = pt.problem_id
		JOIN tags t ON t.id = pt.tag_id
		WHERE pt.tag_id = ? AND t.owner_id = ? AND p.owner_id = ? AND p.deleted_at IS NULL`
	args := []any{tagID.String(), ownerID.String(), ownerID.String()}
	if !after.IsNil() {
		q += ` AND pt.problem_id > ?`
		args = append(args, after.String())
	}
	q += ` ORDER BY pt.problem_id`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := r.q.QueryContext(ctx, q, args...)
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

func (r tagRepo) ProblemTags(ctx context.Context, ownerID, problemID domain.ID) ([]tags.Tag, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+prefixed("t.", tagCols)+` FROM tags t
		JOIN problem_tags pt ON pt.tag_id = t.id
		JOIN problems p ON p.id = pt.problem_id
		WHERE pt.problem_id = ? AND t.owner_id = ? AND p.owner_id = ?
		ORDER BY t.slug, t.id`, problemID.String(), ownerID.String(), ownerID.String())
	if err != nil {
		return nil, err
	}
	var out []tags.Tag
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, r.withAliases(ctx, ownerID, out)
}

func (r tagRepo) SetProblemTags(ctx context.Context, ownerID, problemID domain.ID, tagIDs []domain.ID) error {
	// Check every tag first so a foreign or unknown tag changes nothing even
	// when the caller is not inside a transaction.
	for _, id := range tagIDs {
		var n int
		if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM tags WHERE id = ? AND owner_id = ?`,
			id.String(), ownerID.String()).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return tags.ErrNotFound
		}
	}
	if _, err := r.q.ExecContext(ctx, `DELETE FROM problem_tags WHERE problem_id = ?
		AND problem_id IN (SELECT id FROM problems WHERE id = ? AND owner_id = ?)`,
		problemID.String(), problemID.String(), ownerID.String()); err != nil {
		return err
	}
	for _, id := range tagIDs {
		res, err := r.q.ExecContext(ctx, `INSERT OR IGNORE INTO problem_tags (problem_id, tag_id)
			SELECT id, ? FROM problems WHERE id = ? AND owner_id = ?`,
			id.String(), problemID.String(), ownerID.String())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			// Duplicate id in tagIDs inserts 0 rows too; only a missing problem is an error.
			var c int
			if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM problems WHERE id = ? AND owner_id = ?`,
				problemID.String(), ownerID.String()).Scan(&c); err != nil {
				return err
			}
			if c == 0 {
				return tags.ErrNotFound
			}
		}
	}
	return nil
}
