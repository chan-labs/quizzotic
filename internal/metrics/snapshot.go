// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// MasterySnapshot is one tag's mastery at the start of a study day: the daily
// rollup behind the over-time view.
type MasterySnapshot struct {
	UserID domain.ID
	TagID  domain.ID
	// Date is the study day, YYYY-MM-DD in the user's time zone.
	Date      string
	Recall    float64
	CardsSeen int
}

// SnapshotRepo persists mastery snapshots. Every method is scoped to the owner
// it names, except UserIDs, which the scheduler needs to visit every user.
type SnapshotRepo interface {
	// InsertIfAbsent stores s unless the owner already has a snapshot for
	// (tag, date), reporting whether it inserted. It never overwrites. s.UserID
	// must equal ownerID and the tag must be the owner's, otherwise ErrNotFound
	// is returned and nothing is stored.
	InsertIfAbsent(ctx context.Context, ownerID domain.ID, s MasterySnapshot) (bool, error)

	// List returns the owner's snapshots for the tag with from <= date <= to
	// (inclusive, YYYY-MM-DD; an empty bound is open), ordered by date.
	List(ctx context.Context, ownerID, tagID domain.ID, from, to string) ([]MasterySnapshot, error)

	// ListDate returns every snapshot the owner has for the study day, ordered
	// by tag ID.
	ListDate(ctx context.Context, ownerID domain.ID, date string) ([]MasterySnapshot, error)

	// UserIDs returns every user ID, ascending. It is the one cross-owner read:
	// it exposes no user data, only the IDs the scheduler then acts on one by
	// one.
	UserIDs(ctx context.Context) ([]domain.ID, error)
}

// SettingsSource supplies a user's effective scheduling settings (time zone and
// rollover hour).
type SettingsSource interface {
	Get(ctx context.Context, userID domain.ID) (settings.Settings, error)
}

// SnapshotScope is everything snapshotting reads and writes, bound to one
// transaction.
type SnapshotScope struct {
	Scope     Scope
	Snapshots SnapshotRepo
	Settings  SettingsSource
}

// SnapshotTransactor runs fn atomically over a transaction-bound
// SnapshotScope. The store package adapts a store.Store to it.
type SnapshotTransactor interface {
	InTx(ctx context.Context, fn func(SnapshotScope) error) error
}

// Snapshotter writes the daily mastery snapshots.
//
// The scheduler calls Run, which calls RunOnce on a ticker. RunOnce is
// catch-up shaped rather than edge-triggered: it ensures that every user has a
// snapshot of every active tag for the study day they are in now. The day
// turns over at the user's rollover hour, so the snapshot is written by the
// first pass after the rollover, and a pass that finds the day already
// snapshotted does nothing. A server that was down at the rollover therefore
// writes the day's snapshot as soon as it is back, and running twice (or from
// two processes) never duplicates a row.
type Snapshotter struct {
	tx    SnapshotTransactor
	clock domain.Clock
	log   *slog.Logger
}

// NewSnapshotter builds a Snapshotter over tx. A nil log discards logging.
func NewSnapshotter(tx SnapshotTransactor, clock domain.Clock, log *slog.Logger) *Snapshotter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Snapshotter{tx: tx, clock: clock, log: log}
}

// RunOnce snapshots every user for the study day they are in at clock.Now().
// A failure for one user does not stop the others; the failures are returned
// joined.
func (s *Snapshotter) RunOnce(ctx context.Context) error {
	var users []domain.ID
	if err := s.tx.InTx(ctx, func(sc SnapshotScope) (err error) {
		users, err = sc.Snapshots.UserIDs(ctx)
		return err
	}); err != nil {
		return err
	}
	var errs []error
	for _, u := range users {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := s.SnapshotUser(ctx, u); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SnapshotUser ensures the user has a snapshot of each active tag for the study
// day containing clock.Now(). Mastery is recomputed first so the snapshot
// records recall as of now, not as of the last attempt. A day that is already
// complete is left untouched.
func (s *Snapshotter) SnapshotUser(ctx context.Context, userID domain.ID) error {
	return s.tx.InTx(ctx, func(sc SnapshotScope) error {
		cfg, err := sc.Settings.Get(ctx, userID)
		if err != nil {
			return err
		}
		day, err := cfg.StudyDay(s.clock.Now())
		if err != nil {
			return err
		}
		active, err := sc.Scope.Repo.ActiveTagIDs(ctx, userID)
		if err != nil || len(active) == 0 {
			return err
		}
		have, err := sc.Snapshots.ListDate(ctx, userID, day.Date)
		if err != nil {
			return err
		}
		done := make(map[domain.ID]bool, len(have))
		for _, h := range have {
			done[h.TagID] = true
		}
		complete := true
		for _, id := range active {
			complete = complete && done[id]
		}
		if complete {
			return nil
		}
		if err := NewProjector(sc.Scope, s.clock).Rebuild(ctx, userID); err != nil {
			return err
		}
		rows, err := sc.Scope.Repo.List(ctx, userID)
		if err != nil {
			return err
		}
		wrote := 0
		for _, m := range rows {
			ok, err := sc.Snapshots.InsertIfAbsent(ctx, userID, MasterySnapshot{
				UserID: userID, TagID: m.TagID, Date: day.Date, Recall: m.Recall, CardsSeen: m.CardsSeen})
			if err != nil {
				return err
			}
			if ok {
				wrote++
			}
		}
		if wrote > 0 {
			s.log.InfoContext(ctx, "mastery snapshot", "user", userID.String(), "date", day.Date, "tags", wrote)
		}
		return nil
	})
}

// Run calls RunOnce immediately and then every interval until ctx is done.
// Failures are logged and retried on the next pass.
func (s *Snapshotter) Run(ctx context.Context, interval time.Duration) {
	pass := func() {
		if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "mastery snapshot failed", "err", err)
		}
	}
	pass()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass()
		}
	}
}
