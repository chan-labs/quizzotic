// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"errors"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// lockOwner takes the owner's advisory lock for the rest of the transaction
// q belongs to. One key per owner covers everything that is read to decide a
// write across several statements: tag slugs and mappings, and the
// attempt-derived card and mastery projections. Postgres runs at READ
// COMMITTED, so without it two transactions of the same owner can each miss
// the other's uncommitted change. The lock is released by commit or rollback,
// is re-entrant within a transaction and never blocks other owners. See
// docs/adr/0007-owner-tag-lock.md.
func lockOwner(ctx context.Context, q execer, ownerID domain.ID) error {
	if _, ok := q.(rebinder); !ok {
		return errors.New("postgres: LockOwner needs a transaction")
	} else if !inTx(q) {
		// An advisory xact lock outside a transaction is released at once.
		return errors.New("postgres: LockOwner must run inside WithTx")
	}
	_, err := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))`, "quizzotic:owner:"+ownerID.String())
	return err
}

// LockOwner implements tags.Repo.
func (r tagRepo) LockOwner(ctx context.Context, ownerID domain.ID) error {
	return lockOwner(ctx, r.q, ownerID)
}

// LockOwner implements review.Repo.
func (r cardRepo) LockOwner(ctx context.Context, ownerID domain.ID) error {
	return lockOwner(ctx, r.q, ownerID)
}

// LockOwner implements metrics.Repo.
func (r masteryRepo) LockOwner(ctx context.Context, ownerID domain.ID) error {
	return lockOwner(ctx, r.q, ownerID)
}
