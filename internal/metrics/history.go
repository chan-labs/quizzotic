// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// HistoryProblem is a problem with what a historical replay can know about its
// life: when it was created, when it was last changed (the archive moment for
// an archived problem) and deleted, when each major version was made, and the
// active tags it carries now.
type HistoryProblem struct {
	ID     domain.ID
	Status problems.Status
	// CreatedAt and UpdatedAt are the problem row's timestamps.
	CreatedAt, UpdatedAt time.Time
	// DeletedAt is the soft-delete time; zero while the problem is not deleted.
	DeletedAt time.Time
	// MajorAt holds the creation time of each major version, ascending. The
	// card epoch at a moment is the number of major versions made by then
	// (at least 1), the rule problems.CardEpoch applies to the current one.
	MajorAt []time.Time
	// TagIDs are the active tags on the problem now, ascending.
	TagIDs []domain.ID
}

// LiveAt reports whether the problem counted as a live problem at t: created,
// not yet deleted and not yet archived. The store keeps no status history, so
// an archived problem is taken to have been live until its last update (the
// archive), and a problem that is live now is taken to have been live since it
// was created, even if it started as a draft.
func (p HistoryProblem) LiveAt(t time.Time) bool {
	switch {
	case p.Status == problems.StatusDraft, p.CreatedAt.After(t):
		return false
	case !p.DeletedAt.IsZero() && !p.DeletedAt.After(t):
		return false
	case p.Status == problems.StatusArchived && !p.UpdatedAt.After(t):
		return false
	}
	return true
}

// EpochAt is the card epoch of the problem at t.
func (p HistoryProblem) EpochAt(t time.Time) int {
	n := 0
	for _, m := range p.MajorAt {
		if !m.After(t) {
			n++
		}
	}
	return max(n, 1)
}

// HistoryTag is an active tag and when it was created.
type HistoryTag struct {
	ID        domain.ID
	CreatedAt time.Time
}

// cardTimeline is one card's state after each of its attempts.
type cardTimeline struct {
	at    []time.Time
	cards []review.Card
}

// cardAt returns the card as of t (counting attempts answered at or before t), or
// nil when it had no scheduled attempt by then.
func (tl *cardTimeline) cardAt(t time.Time) *review.Card {
	if tl == nil {
		return nil
	}
	i := sort.Search(len(tl.at), func(i int) bool { return tl.at[i].After(t) }) - 1
	if i < 0 || tl.cards[i].Reps == 0 {
		return nil
	}
	return &tl.cards[i]
}

type cardKey struct {
	problem domain.ID
	epoch   int
}

// ReplaySnapshots computes the snapshots the daily job would have written at
// the start of each of the given study days. It is pure.
//
// For a day starting at T it reconstructs the state of the world at T and
// computes each tag's mastery exactly as the live projection does (Compute):
//
//   - a problem counts if it was live at T (HistoryProblem.LiveAt) and carries
//     the tag in the problem's current tag set;
//   - its card is the fold of the attempts of the card epoch current at T that
//     were answered at or before T, using each attempt's stored rating;
//   - recall is predicted at T.
//
// log must be ordered by problem, card epoch, answered_at, ID. A tag gets a
// snapshot for a day if it was created by the end of that day (the daily job
// snapshots a tag created mid-day on its next pass) or already had a live
// problem, so a tag born of a split or merge gets the history of its problems
// rather than starting empty. Snapshots are ordered by date, then tag.
func ReplaySnapshots(p fsrs.Params, userID domain.ID, days []settings.StudyDay, tags []HistoryTag,
	probs []HistoryProblem, log []attempts.Attempt) []MasterySnapshot {
	timelines := map[cardKey]*cardTimeline{}
	for i := 0; i < len(log); {
		j := i
		for j < len(log) && log[j].ProblemID == log[i].ProblemID && log[j].CardEpoch == log[i].CardEpoch {
			j++
		}
		tl := &cardTimeline{}
		var c review.Card
		for _, a := range log[i:j] {
			c, _ = review.FoldStep(p, c, a)
			tl.at = append(tl.at, a.AnsweredAt)
			tl.cards = append(tl.cards, c)
		}
		timelines[cardKey{log[i].ProblemID, log[i].CardEpoch}] = tl
		i = j
	}

	// Cards are summed in problem-ID order, as the live query orders them.
	probs = slices.Clone(probs)
	slices.SortFunc(probs, func(a, b HistoryProblem) int { return compareID(a.ID, b.ID) })
	inScope := make(map[domain.ID]bool, len(tags))
	for _, t := range tags {
		inScope[t.ID] = true
	}

	var out []MasterySnapshot
	for _, day := range days {
		T := day.Start
		byTag := make(map[domain.ID][]TagCard, len(tags))
		for _, pr := range probs {
			if !pr.LiveAt(T) {
				continue
			}
			var card *review.Card
			if c := timelines[cardKey{pr.ID, pr.EpochAt(T)}].cardAt(T); c != nil {
				cp := *c
				cp.UserID, cp.ProblemID, cp.Epoch = userID, pr.ID, pr.EpochAt(T)
				card = &cp
			}
			for _, id := range pr.TagIDs {
				if inScope[id] {
					byTag[id] = append(byTag[id], TagCard{TagID: id, ProblemID: pr.ID, Card: card})
				}
			}
		}
		for _, t := range tags {
			cards := byTag[t.ID]
			if !t.CreatedAt.Before(day.End) && len(cards) == 0 {
				continue
			}
			m := Compute(p, T, userID, t.ID, cards, time.Time{})
			out = append(out, MasterySnapshot{UserID: userID, TagID: t.ID, Date: day.Date,
				Recall: m.Recall, CardsSeen: m.CardsSeen})
		}
	}
	return out
}

func compareID(a, b domain.ID) int {
	switch sa, sb := a.String(), b.String(); {
	case sa < sb:
		return -1
	case sa > sb:
		return 1
	}
	return 0
}

// BackfillResult is what a backfill wrote.
type BackfillResult struct {
	// Tags is how many tags were rewritten, Days how many study days they
	// span, and Snapshots how many rows were written.
	Tags, Days, Snapshots int
	// Skipped lists requested tags that are not active tags of the owner
	// (merged, retired or unknown); their snapshots were left alone.
	Skipped []domain.ID
}

// Backfill rewrites the owner's mastery snapshots by replaying the attempt log
// and stored problem data to every study day, from the earliest data up to the
// study day containing now. tagIDs limits the rewrite to those tags (the
// tag-operation case); empty means every active tag.
//
// It follows the daily job's conventions exactly: the study days are the
// owner's current time zone and rollover hour (Settings.Get), a day's snapshot
// is dated by the day and records the state at the day's rollover instant, and
// recall is predicted with the owner's current scheduler parameters. Existing
// snapshots of the rewritten tags are replaced; those of tags that are no
// longer active are kept.
//
// What the replay cannot know, because the store keeps no history of it, is
// resolved as follows: problem-to-tag mappings and tag existence use the
// current ones (so history follows merges and splits, as the spec intends);
// status changes other than archive and delete are not seen (see LiveAt); a
// late rating counts from the attempt's own time, not from when it was given;
// and past scheduler parameters and time zone settings are not known.
//
// Backfill runs inside sc's transaction, so a caller that is already in one
// (a tag operation) gets history rewritten atomically with its other changes.
func Backfill(ctx context.Context, sc SnapshotScope, now time.Time, userID domain.ID, tagIDs []domain.ID) (BackfillResult, error) {
	var res BackfillResult
	cfg, err := sc.Settings.Get(ctx, userID)
	if err != nil {
		return res, err
	}
	loc, err := cfg.Location()
	if err != nil {
		return res, err
	}
	active, err := sc.Scope.Repo.ActiveTags(ctx, userID)
	if err != nil {
		return res, err
	}
	var tags []HistoryTag
	if len(tagIDs) == 0 {
		tags = active
	} else {
		seen := map[domain.ID]bool{}
		for _, id := range tagIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			i := slices.IndexFunc(active, func(t HistoryTag) bool { return t.ID == id })
			if i < 0 {
				res.Skipped = append(res.Skipped, id)
				continue
			}
			tags = append(tags, active[i])
		}
		slices.SortFunc(tags, func(a, b HistoryTag) int { return compareID(a.ID, b.ID) })
	}
	if len(tags) == 0 {
		return res, nil
	}
	params, err := sc.Scope.Params.Params(ctx, userID)
	if err != nil {
		return res, err
	}
	if err := params.Validate(); err != nil {
		return res, err
	}
	probs, err := sc.Scope.Repo.HistoryProblems(ctx, userID)
	if err != nil {
		return res, err
	}
	log, err := sc.Scope.Repo.HistoryAttempts(ctx, userID)
	if err != nil {
		return res, err
	}

	first := now
	for _, t := range tags {
		first = minTime(first, t.CreatedAt)
	}
	for _, p := range probs {
		first = minTime(first, p.CreatedAt)
	}
	for _, a := range log {
		first = minTime(first, a.AnsweredAt)
	}
	var days []settings.StudyDay
	last := settings.StudyDayAt(now, loc, cfg.RolloverHour)
	for d := settings.StudyDayAt(first, loc, cfg.RolloverHour); ; {
		days = append(days, d)
		if d.Date == last.Date {
			break
		}
		d = settings.StudyDayAt(d.End, loc, cfg.RolloverHour)
	}

	snaps := ReplaySnapshots(params, userID, days, tags, probs, log)
	ids := make([]domain.ID, len(tags))
	for i, t := range tags {
		ids[i] = t.ID
	}
	if err := sc.Snapshots.DeleteTags(ctx, userID, ids); err != nil {
		return res, err
	}
	for _, s := range snaps {
		if err := sc.Snapshots.Put(ctx, userID, s); err != nil {
			return res, err
		}
	}
	res.Tags, res.Days, res.Snapshots = len(tags), len(days), len(snaps)
	return res, nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// BackfillUser rewrites one user's snapshot history (see Backfill) in one
// transaction, as of the Snapshotter's clock. tagIDs limits it to those tags.
// An unknown user returns ErrNotFound.
func (s *Snapshotter) BackfillUser(ctx context.Context, userID domain.ID, tagIDs []domain.ID) (BackfillResult, error) {
	var res BackfillResult
	err := s.tx.InTx(ctx, func(sc SnapshotScope) error {
		users, err := sc.Snapshots.UserIDs(ctx)
		if err != nil {
			return err
		}
		if !slices.Contains(users, userID) {
			return ErrNotFound
		}
		res, err = Backfill(ctx, sc, s.clock.Now(), userID, tagIDs)
		return err
	})
	return res, err
}

// BackfillAll rewrites every user's snapshot history, one transaction per
// user. A failure for one user does not stop the others; the failures are
// returned joined, with the summed result of the users that succeeded.
func (s *Snapshotter) BackfillAll(ctx context.Context) (users int, total BackfillResult, err error) {
	var ids []domain.ID
	if err := s.tx.InTx(ctx, func(sc SnapshotScope) (err error) {
		ids, err = sc.Snapshots.UserIDs(ctx)
		return err
	}); err != nil {
		return 0, total, err
	}
	var errs []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return users, total, errors.Join(append(errs, err)...)
		}
		r, err := s.BackfillUser(ctx, id, nil)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		users++
		total.Tags += r.Tags
		total.Days += r.Days
		total.Snapshots += r.Snapshots
	}
	return users, total, errors.Join(errs...)
}
