// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// historyCmd is `quizzotic admin rebuild --history`: it rewrites the mastery
// snapshots of one user (--user), one tag of that user (--tag), or, by
// default, every user, by replaying the attempt log to each past study day.
// The replay itself is metrics.Backfill; this only picks the scope.
func historyCmd(ctx context.Context, st store.Store, cards store.Cards, user domain.ID, tag string,
	logger *slog.Logger, stdout, stderr io.Writer) int {
	snapper := store.NewMastery(cards, domain.SystemClock{}).Snapshotter(st, logger)

	if user.IsNil() {
		users, sum, err := snapper.BackfillAll(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "quizzotic: rebuild history: %v (rewrote %d user(s) before the error; rerunning is safe)\n", err, users)
			return 1
		}
		fmt.Fprintf(stdout, "rewrote %d snapshot(s) of %d tag(s) over %d day(s) for %d user(s)\n",
			sum.Snapshots, sum.Tags, sum.Days, users)
		return 0
	}

	var only []domain.ID
	if tag != "" {
		id, err := resolveTag(ctx, st, user, tag)
		if err != nil {
			fmt.Fprintf(stderr, "quizzotic: --tag: %v\n", err)
			return 1
		}
		only = []domain.ID{id}
	}
	res, err := snapper.BackfillUser(ctx, user, only)
	switch {
	case errors.Is(err, metrics.ErrNotFound):
		fmt.Fprintf(stderr, "quizzotic: no such user %s\n", user)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "quizzotic: rebuild history: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "rewrote %d snapshot(s) of %d tag(s) over %d day(s) for 1 user(s)\n",
		res.Snapshots, res.Tags, res.Days)
	return 0
}

// resolveTag finds the user's active tag named by an ID or by a slug (an old
// slug resolves through its alias; a merged tag, to the tag it was merged into).
func resolveTag(ctx context.Context, st store.Store, user domain.ID, ref string) (domain.ID, error) {
	repo := st.Tags()
	var t tags.Tag
	var err error
	if id, perr := domain.ParseID(ref); perr == nil {
		t, err = repo.Get(ctx, user, id)
	} else if t, err = repo.GetBySlug(ctx, user, ref); errors.Is(err, tags.ErrNotFound) {
		t, err = repo.GetByAlias(ctx, user, ref)
	}
	if errors.Is(err, tags.ErrNotFound) {
		return domain.ID{}, fmt.Errorf("no such tag %q", ref)
	}
	if err != nil {
		return domain.ID{}, err
	}
	if t.Status == tags.StatusMerged && !t.MergedInto.IsNil() {
		return t.MergedInto, nil
	}
	if t.Status != tags.StatusActive {
		return domain.ID{}, fmt.Errorf("tag %q is %s, so it has no snapshot history to rewrite", ref, t.Status)
	}
	return t.ID, nil
}
