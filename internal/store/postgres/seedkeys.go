// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/seed"
)

// SeedKeys implements store.Store.
func (s *Store) SeedKeys() seed.KeyRepo { return seedKeyRepo{s.q} }

type seedKeyRepo struct{ q execer }

func (r seedKeyRepo) Get(ctx context.Context, ownerID domain.ID, kind seed.Kind, key string) (seed.Entry, error) {
	var id string
	var e seed.Entry
	err := r.q.QueryRowContext(ctx, `SELECT entity_id, digest FROM seed_keys
		WHERE owner_id = ? AND kind = ? AND key = ?`, ownerID.String(), string(kind), key).Scan(&id, &e.Digest)
	if errors.Is(err, sql.ErrNoRows) {
		return seed.Entry{}, seed.ErrNotFound
	}
	if err != nil {
		return seed.Entry{}, err
	}
	if e.EntityID, err = domain.ParseID(id); err != nil {
		return seed.Entry{}, err
	}
	return e, nil
}

func (r seedKeyRepo) Put(ctx context.Context, ownerID domain.ID, kind seed.Kind, key string, e seed.Entry, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO seed_keys (owner_id, kind, key, entity_id, digest, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (owner_id, kind, key) DO UPDATE SET
			entity_id = excluded.entity_id, digest = excluded.digest, updated_at = excluded.updated_at`,
		ownerID.String(), string(kind), key, e.EntityID.String(), e.Digest, ts(at))
	return err
}
