// SPDX-License-Identifier: Apache-2.0

package tags_test

import (
	"context"
	"errors"
	"math/rand"
	"testing"

	"github.com/chan-labs/quizzotic/internal/tags"
)

// dropMastery is a transactor whose Repos have no mastery rebuilder and did
// not opt out: a production wiring that forgot it.
type dropMastery struct {
	tags.Transactor
	on *bool
}

func (d dropMastery) InTx(ctx context.Context, fn func(tags.Repos) error) error {
	return d.Transactor.InTx(ctx, func(r tags.Repos) error {
		if *d.on {
			r.Mastery, r.SkipMastery = nil, false
		}
		return fn(r)
	})
}

// A tag change without a mastery rebuilder fails loudly instead of leaving
// mastery stale.
func TestNoMasteryFailsLoudly(t *testing.T) {
	var on bool
	f := newRebuildFx(t, func(tx tags.Transactor) tags.Transactor { return dropMastery{tx, &on} })
	a := f.tag("a")
	p := f.problem(rand.New(rand.NewSource(1)))
	on = true
	_, err := f.svc.SetProblemTags(f.ctx, f.owner, p, []string{a.ID.String()})
	if !errors.Is(err, tags.ErrNoMastery) {
		t.Fatalf("SetProblemTags without mastery = %v, want ErrNoMastery", err)
	}
}
