// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// LockOwner does nothing on SQLite: every write transaction starts with BEGIN
// IMMEDIATE (see Open), so units of work already run one at a time. It
// implements tags.Repo, review.Repo and metrics.Repo. See
// docs/adr/0007-owner-tag-lock.md.
func (r tagRepo) LockOwner(ctx context.Context, ownerID domain.ID) error { return nil }

// LockOwner implements review.Repo.
func (r cardRepo) LockOwner(ctx context.Context, ownerID domain.ID) error { return nil }

// LockOwner implements metrics.Repo.
func (r masteryRepo) LockOwner(ctx context.Context, ownerID domain.ID) error { return nil }
