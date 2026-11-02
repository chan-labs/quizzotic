// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// Projector maintains the mastery projection inside one transaction-bound
// Scope. The store binds one to each attempt transaction, so mastery is
// written atomically with the attempt and card that cause it.
type Projector struct {
	sc    Scope
	clock domain.Clock
}

// NewProjector builds a Projector over sc that predicts recall at clock.Now().
func NewProjector(sc Scope, clock domain.Clock) *Projector { return &Projector{sc, clock} }

// UpdateProblem recomputes the mastery rows of every tag on the problem. It is
// the incremental update after an attempt (or a card change) on the problem;
// the card projection must already reflect the change.
func (p *Projector) UpdateProblem(ctx context.Context, ownerID, problemID domain.ID) error {
	if err := p.sc.Repo.LockOwner(ctx, ownerID); err != nil {
		return err
	}
	ids, err := p.sc.Repo.ProblemTagIDs(ctx, ownerID, problemID)
	if err != nil || len(ids) == 0 {
		return err
	}
	return p.UpdateTags(ctx, ownerID, ids)
}

// UpdateTags recomputes the rows of the given tags. A tag that is no longer
// active (merged, retired, or gone) has its row removed.
func (p *Projector) UpdateTags(ctx context.Context, ownerID domain.ID, tagIDs []domain.ID) error {
	if err := p.sc.Repo.LockOwner(ctx, ownerID); err != nil {
		return err
	}
	active, err := p.sc.Repo.ActiveTagIDs(ctx, ownerID)
	if err != nil {
		return err
	}
	isActive := make(map[domain.ID]bool, len(active))
	for _, id := range active {
		isActive[id] = true
	}
	var want []domain.ID
	seen := map[domain.ID]bool{}
	for _, id := range tagIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if isActive[id] {
			want = append(want, id)
		} else if err := p.sc.Repo.Delete(ctx, ownerID, id); err != nil {
			return err
		}
	}
	if len(want) == 0 {
		return nil
	}
	return p.compute(ctx, ownerID, want)
}

// Rebuild drops every mastery row of the owner and recomputes one row for each
// active tag. It is the whole projection; incremental updates must always
// agree with it.
func (p *Projector) Rebuild(ctx context.Context, ownerID domain.ID) error {
	if err := p.sc.Repo.LockOwner(ctx, ownerID); err != nil {
		return err
	}
	if err := p.sc.Repo.DeleteAll(ctx, ownerID); err != nil {
		return err
	}
	active, err := p.sc.Repo.ActiveTagIDs(ctx, ownerID)
	if err != nil || len(active) == 0 {
		return err
	}
	return p.compute(ctx, ownerID, active)
}

// compute writes a row for each of the active tags in ids.
func (p *Projector) compute(ctx context.Context, ownerID domain.ID, ids []domain.ID) error {
	params, err := p.sc.Params.Params(ctx, ownerID)
	if err != nil {
		return err
	}
	if err := params.Validate(); err != nil {
		return err
	}
	cards, err := p.sc.Repo.TagCards(ctx, ownerID, ids)
	if err != nil {
		return err
	}
	last, err := p.sc.Repo.LastAttempts(ctx, ownerID, ids)
	if err != nil {
		return err
	}
	byTag := make(map[domain.ID][]TagCard, len(ids))
	for _, tc := range cards {
		byTag[tc.TagID] = append(byTag[tc.TagID], tc)
	}
	now := p.clock.Now()
	for _, id := range ids {
		m := Compute(params, now, ownerID, id, byTag[id], last[id])
		if err := p.sc.Repo.Put(ctx, ownerID, m); err != nil {
			return err
		}
	}
	return nil
}

// Service runs the projection's own operations, each in one transaction.
type Service struct {
	tx    Transactor
	clock domain.Clock
}

// NewService builds a Service over tx.
func NewService(tx Transactor, clock domain.Clock) *Service { return &Service{tx, clock} }

// StatsOptions sizes the user-level metrics; zero fields take the defaults.
type StatsOptions struct {
	// RetentionDays is the true-retention window in days.
	RetentionDays int
	// ActivityDays is how many study days of activity to report.
	ActivityDays int
}

// UserStats are the user-level metrics at one moment.
type UserStats struct {
	// Day is the study day containing the moment.
	Day settings.StudyDay
	// RetentionDays is the window the retention figures cover.
	RetentionDays int
	// Retention is true retention over all of the user's reviews; ByTag has
	// one entry per tag that was reviewed in the window.
	Retention Retention
	ByTag     map[domain.ID]Retention
	// DueLoad is today and the next DueLoadDays study days.
	DueLoad []DueDay
	// Activity is the daily activity, oldest first, ending today.
	Activity []DayActivity
}

// UserStats computes the user's true retention, due load and daily activity as
// of the service clock, in the day boundaries of cfg (the user's effective
// settings), reading one consistent snapshot.
func (s *Service) UserStats(ctx context.Context, userID domain.ID, cfg settings.Settings, opts StatsOptions) (UserStats, error) {
	var st UserStats
	err := s.tx.InTx(ctx, func(sc Scope) (err error) {
		st, err = userStatsIn(ctx, sc, s.clock.Now(), userID, cfg, opts)
		return err
	})
	return st, err
}

// userStatsIn is UserStats over a caller's transaction-bound scope, so that
// readers computing more than the user-level figures see one snapshot.
func userStatsIn(ctx context.Context, sc Scope, now time.Time, userID domain.ID, cfg settings.Settings, opts StatsOptions) (UserStats, error) {
	if opts.RetentionDays <= 0 {
		opts.RetentionDays = DefaultRetentionDays
	}
	if opts.ActivityDays <= 0 {
		opts.ActivityDays = DefaultActivityDays
	}
	loc, err := cfg.Location()
	if err != nil {
		return UserStats{}, err
	}
	today := settings.StudyDayAt(now, loc, cfg.RolloverHour)
	activity := Activity(nil, today, loc, cfg.RolloverHour, opts.ActivityDays)
	window := time.Duration(opts.RetentionDays) * 24 * time.Hour
	since := now.Add(-window)
	if start := activity[0].Start; start.Before(since) {
		since = start
	}
	horizon := DueLoad(nil, today, loc, cfg.RolloverHour)

	st := UserStats{Day: today, RetentionDays: opts.RetentionDays}
	params, err := sc.Params.Params(ctx, userID)
	if err != nil {
		return UserStats{}, err
	}
	if err := params.Validate(); err != nil {
		return UserStats{}, err
	}
	log, err := sc.Repo.AttemptLog(ctx, userID, since)
	if err != nil {
		return UserStats{}, err
	}
	tags, err := sc.Repo.ProblemTags(ctx, userID)
	if err != nil {
		return UserStats{}, err
	}
	due, err := sc.Repo.DueTimes(ctx, userID, horizon[len(horizon)-1].End)
	if err != nil {
		return UserStats{}, err
	}
	ev := Replay(params, log, tags)
	st.Retention, st.ByTag = TrueRetention(ev, now, window)
	st.DueLoad = DueLoad(due, today, loc, cfg.RolloverHour)
	st.Activity = Activity(ev, today, loc, cfg.RolloverHour, opts.ActivityDays)
	return st, nil
}

// RebuildUser drops and recomputes all of one user's mastery rows.
func (s *Service) RebuildUser(ctx context.Context, userID domain.ID) error {
	return s.tx.InTx(ctx, func(sc Scope) error { return NewProjector(sc, s.clock).Rebuild(ctx, userID) })
}
