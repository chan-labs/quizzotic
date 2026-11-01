// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/settings"
)

func runSettings(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})

	t.Run("new user has no overrides", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "ada")
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		got, err := s.Settings().Get(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got != (settings.Overrides{}) {
			t.Fatalf("got %+v, want zero", got)
		}
	})

	t.Run("replace round trips and clears", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "ada")
		steps := []string{"2m", "15m"}
		u.Scheduling = auth.Scheduling{LearningSteps: steps}
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		ret, wtb := 0.85, 1.5
		npd, mrd, ni, roh, tz := 5, 100, 3, 6, "Europe/London"
		full := settings.Overrides{DesiredRetention: &ret, NewPerDay: &npd, MaxReviewsPerDay: &mrd,
			NewInterval: &ni, WeakTagBoost: &wtb, RolloverHour: &roh, Timezone: &tz}
		if err := s.Settings().Replace(ctx, u.ID, full); err != nil {
			t.Fatal(err)
		}
		got, err := s.Settings().Get(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if *got.DesiredRetention != ret || *got.NewPerDay != npd || *got.MaxReviewsPerDay != mrd ||
			*got.NewInterval != ni || *got.WeakTagBoost != wtb || *got.RolloverHour != roh || *got.Timezone != tz {
			t.Fatalf("got %+v", got)
		}
		// Other user columns survive.
		usr, _ := s.Users().Get(ctx, u.ID)
		if usr.DisplayName != "ada" || len(usr.Scheduling.LearningSteps) != 2 {
			t.Fatalf("user clobbered: %+v", usr)
		}
		// A zero value is a real override, distinct from nil.
		zero := 0
		if err := s.Settings().Replace(ctx, u.ID, settings.Overrides{NewPerDay: &zero, RolloverHour: &zero}); err != nil {
			t.Fatal(err)
		}
		got, _ = s.Settings().Get(ctx, u.ID)
		if got.NewPerDay == nil || *got.NewPerDay != 0 || got.RolloverHour == nil || *got.RolloverHour != 0 ||
			got.DesiredRetention != nil || got.Timezone != nil {
			t.Fatalf("after replace: %+v", got)
		}
	})

	t.Run("missing user", func(t *testing.T) {
		s, _ := newStore(t)
		if _, err := s.Settings().Get(ctx, ids.New()); !errors.Is(err, settings.ErrNotFound) {
			t.Fatalf("get: err = %v", err)
		}
		if err := s.Settings().Replace(ctx, ids.New(), settings.Overrides{}); !errors.Is(err, settings.ErrNotFound) {
			t.Fatalf("replace: err = %v", err)
		}
	})

	t.Run("replace is owner scoped", func(t *testing.T) {
		s, _ := newStore(t)
		a, b := newUser(ids, "a"), newUser(ids, "b")
		for _, u := range []auth.User{a, b} {
			if err := s.Users().Create(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
		tz := "Asia/Tokyo"
		if err := s.Settings().Replace(ctx, a.ID, settings.Overrides{Timezone: &tz}); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Settings().Get(ctx, b.ID)
		if got.Timezone != nil {
			t.Fatalf("user b saw user a's settings: %+v", got)
		}
	})
}
