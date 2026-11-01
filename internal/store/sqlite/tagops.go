// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// TagOperations implements store.Store.
func (s *Store) TagOperations() tags.OpRepo { return tagOpRepo{s.q} }

type tagOpRepo struct{ q execer }

const tagOpCols = `id, owner_id, kind, payload, status,
	proposed_by_kind, proposed_by_user_id, proposed_by_client_id,
	decided_by_kind, decided_by_user_id, decided_by_client_id,
	before_snapshot, created_at, decided_at, applied_at`

func nullMillis(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return Millis(t)
}

func encodeSnapshot(s *tags.Snapshot) (any, error) {
	if s == nil {
		return nil, nil
	}
	c := *s
	if c.Tags == nil {
		c.Tags = []tags.SnapshotTag{}
	}
	if c.Mappings == nil {
		c.Mappings = []tags.Mapping{}
	}
	b, err := json.Marshal(c)
	return string(b), err
}

func (r tagOpRepo) Create(ctx context.Context, ownerID domain.ID, op tags.Operation) error {
	if op.OwnerID != ownerID {
		return errors.New("tags: operation owner does not match owner id")
	}
	if !op.Kind.Valid() || !op.Status.Valid() {
		return errors.New("tags: invalid operation kind or status")
	}
	snap, err := encodeSnapshot(op.Snapshot)
	if err != nil {
		return err
	}
	var decKind any
	if op.Status != tags.OpProposed {
		decKind = string(op.DecidedBy.Kind)
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO tag_operations (`+tagOpCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.ID.String(), ownerID.String(), string(op.Kind), string(op.Payload), string(op.Status),
		string(op.ProposedBy.Kind), actorUser(op.ProposedBy), op.ProposedBy.ClientID,
		decKind, actorUser(op.DecidedBy), op.DecidedBy.ClientID,
		snap, Millis(op.CreatedAt), nullMillis(op.DecidedAt), nullMillis(op.AppliedAt))
	return err
}

func scanTagOp(r scanner) (tags.Operation, error) {
	var (
		op                               tags.Operation
		id, owner, kind, payload, status string
		pKind, pUser, pClient            string
		dKind                            sql.NullString
		dUser, dClient                   string
		snap                             sql.NullString
		created                          int64
		decided, applied                 sql.NullInt64
	)
	if err := r.Scan(&id, &owner, &kind, &payload, &status, &pKind, &pUser, &pClient,
		&dKind, &dUser, &dClient, &snap, &created, &decided, &applied); err != nil {
		return op, err
	}
	var err error
	if op.ID, err = domain.ParseID(id); err != nil {
		return op, err
	}
	if op.OwnerID, err = domain.ParseID(owner); err != nil {
		return op, err
	}
	op.Kind, op.Payload, op.Status = tags.OpKind(kind), json.RawMessage(payload), tags.OpStatus(status)
	op.ProposedBy = domain.Actor{Kind: domain.ActorKind(pKind), ClientID: pClient}
	if op.ProposedBy.UserID, err = parseActorUser(pUser); err != nil {
		return op, err
	}
	if dKind.Valid {
		op.DecidedBy = domain.Actor{Kind: domain.ActorKind(dKind.String), ClientID: dClient}
		if op.DecidedBy.UserID, err = parseActorUser(dUser); err != nil {
			return op, err
		}
	}
	if snap.Valid {
		var s tags.Snapshot
		if err := json.Unmarshal([]byte(snap.String), &s); err != nil {
			return op, err
		}
		op.Snapshot = &s
	}
	op.CreatedAt = FromMillis(created)
	if decided.Valid {
		op.DecidedAt = FromMillis(decided.Int64)
	}
	if applied.Valid {
		op.AppliedAt = FromMillis(applied.Int64)
	}
	return op, nil
}

func (r tagOpRepo) Get(ctx context.Context, ownerID, id domain.ID) (tags.Operation, error) {
	op, err := scanTagOp(r.q.QueryRowContext(ctx, `SELECT `+tagOpCols+` FROM tag_operations
		WHERE owner_id = ? AND id = ?`, ownerID.String(), id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return tags.Operation{}, tags.ErrOpNotFound
	}
	return op, err
}

func (r tagOpRepo) List(ctx context.Context, ownerID domain.ID, f tags.OpFilter) ([]tags.Operation, error) {
	q := `SELECT ` + tagOpCols + ` FROM tag_operations WHERE owner_id = ?`
	args := []any{ownerID.String()}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, string(f.Status))
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
	var out []tags.Operation
	for rows.Next() {
		op, err := scanTagOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (r tagOpRepo) Decide(ctx context.Context, ownerID, id domain.ID, d tags.Decision) error {
	if d.Status != tags.OpApplied && d.Status != tags.OpRejected {
		return errors.New("tags: decision must be applied or rejected")
	}
	if d.Status == tags.OpApplied && (d.Snapshot == nil || d.AppliedAt.IsZero()) {
		return errors.New("tags: applied decision needs a snapshot and applied time")
	}
	snap, err := encodeSnapshot(d.Snapshot)
	if err != nil {
		return err
	}
	res, err := r.q.ExecContext(ctx, `UPDATE tag_operations SET status = ?,
		decided_by_kind = ?, decided_by_user_id = ?, decided_by_client_id = ?,
		before_snapshot = ?, decided_at = ?, applied_at = ?
		WHERE owner_id = ? AND id = ? AND status = 'proposed'`,
		string(d.Status), string(d.DecidedBy.Kind), actorUser(d.DecidedBy), d.DecidedBy.ClientID,
		snap, Millis(d.DecidedAt), nullMillis(d.AppliedAt), ownerID.String(), id.String())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	if _, err := r.Get(ctx, ownerID, id); err != nil {
		return err
	}
	return tags.ErrOpNotProposed
}

func (r tagOpRepo) CaptureSnapshot(ctx context.Context, ownerID domain.ID, tagIDs []domain.ID) (tags.Snapshot, error) {
	snap := tags.Snapshot{Tags: []tags.SnapshotTag{}, Mappings: []tags.Mapping{}}
	ids := slices.Clone(tagIDs)
	slices.SortFunc(ids, func(a, b domain.ID) int { return strings.Compare(a.String(), b.String()) })
	ids = slices.Compact(ids)
	tr := tagRepo(r)
	for _, id := range ids {
		t, err := tr.Get(ctx, ownerID, id)
		if err != nil {
			return tags.Snapshot{}, err
		}
		snap.Tags = append(snap.Tags, tags.SnapshotTagOf(t))
	}
	if len(ids) == 0 {
		return snap, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{ownerID.String()}
	for _, id := range ids {
		args = append(args, id.String())
	}
	rows, err := r.q.QueryContext(ctx, `SELECT pt.problem_id, pt.tag_id FROM problem_tags pt
		JOIN problems p ON p.id = pt.problem_id
		WHERE p.owner_id = ? AND pt.tag_id IN (`+marks+`)
		ORDER BY pt.problem_id, pt.tag_id`, args...)
	if err != nil {
		return tags.Snapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var p, t string
		if err := rows.Scan(&p, &t); err != nil {
			return tags.Snapshot{}, err
		}
		m := tags.Mapping{}
		if m.ProblemID, err = domain.ParseID(p); err != nil {
			return tags.Snapshot{}, err
		}
		if m.TagID, err = domain.ParseID(t); err != nil {
			return tags.Snapshot{}, err
		}
		snap.Mappings = append(snap.Mappings, m)
	}
	return snap, rows.Err()
}
