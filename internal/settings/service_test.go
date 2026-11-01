// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/domain"
)

type memRepo struct {
	users map[domain.ID]Overrides
}

func (m *memRepo) Get(_ context.Context, id domain.ID) (Overrides, error) {
	o, ok := m.users[id]
	if !ok {
		return Overrides{}, ErrNotFound
	}
	return o, nil
}

func (m *memRepo) Replace(_ context.Context, id domain.ID, o Overrides) error {
	if _, ok := m.users[id]; !ok {
		return ErrNotFound
	}
	m.users[id] = o
	return nil
}

type memTx struct{ r *memRepo }

func (m memTx) InTx(_ context.Context, fn func(Repos) error) error {
	return fn(Repos{Settings: m.r})
}

type recorder struct {
	calls []float64
	err   error
}

func (r *recorder) RecomputeCards(_ context.Context, _ domain.ID, ret float64) error {
	r.calls = append(r.calls, ret)
	return r.err
}

func setup(t *testing.T, opts ...Option) (*Service, *memRepo, domain.ID) {
	t.Helper()
	id := domain.NewIDGenerator(domain.SystemClock{}).New()
	repo := &memRepo{users: map[domain.ID]Overrides{id: {}}}
	def := DefaultsFromConfig(config.Default().Scheduling)
	return NewService(memTx{repo}, def, opts...), repo, id
}

func TestDefaultsMatchSpec(t *testing.T) {
	svc, _, id := setup(t)
	got, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{DesiredRetention: 0.90, NewPerDay: 20, MaxReviewsPerDay: 200, NewInterval: 4,
		WeakTagBoost: 1.0, RolloverHour: 4, Timezone: "America/New_York"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestUpdateOverridesAndClears(t *testing.T) {
	svc, _, id := setup(t)
	ctx := context.Background()
	tz, roh, npd := "Europe/Paris", 5, 0
	got, err := svc.Update(ctx, id, Overrides{Timezone: &tz, RolloverHour: &roh, NewPerDay: &npd})
	if err != nil {
		t.Fatal(err)
	}
	if got.Timezone != tz || got.RolloverHour != 5 || got.NewPerDay != 0 || got.MaxReviewsPerDay != 200 {
		t.Fatalf("got %+v", got)
	}
	got, err = svc.Update(ctx, id, Overrides{})
	if err != nil || got.Timezone != "America/New_York" || got.NewPerDay != 20 {
		t.Fatalf("cleared: %+v err=%v", got, err)
	}
	if _, err := svc.Update(ctx, domain.NewIDGenerator(domain.SystemClock{}).New(), Overrides{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
}

func TestUpdateValidation(t *testing.T) {
	svc, repo, id := setup(t)
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }
	s := func(v string) *string { return &v }
	bad := map[string]Overrides{
		"retention 0":     {DesiredRetention: f(0)},
		"retention 1":     {DesiredRetention: f(1)},
		"retention nan":   {DesiredRetention: f(nan())},
		"new_per_day -1":  {NewPerDay: i(-1)},
		"max reviews -1":  {MaxReviewsPerDay: i(-1)},
		"new_interval 0":  {NewInterval: i(0)},
		"weak boost -0.1": {WeakTagBoost: f(-0.1)},
		"rollover 24":     {RolloverHour: i(24)},
		"rollover -1":     {RolloverHour: i(-1)},
		"tz empty":        {Timezone: s("")},
		"tz Local":        {Timezone: s("Local")},
		"tz garbage":      {Timezone: s("Mars/Olympus_Mons")},
	}
	for name, o := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Update(context.Background(), id, o)
			var ve *ValidationError
			if !errors.Is(err, ErrInvalid) || !errors.As(err, &ve) || len(ve.Fields) != 1 {
				t.Fatalf("err = %v", err)
			}
			if repo.users[id] != (Overrides{}) {
				t.Fatalf("invalid update was stored: %+v", repo.users[id])
			}
		})
	}
	ok := Overrides{DesiredRetention: f(0.97), RolloverHour: i(0), NewPerDay: i(0), NewInterval: i(1), Timezone: s("UTC")}
	if _, err := svc.Update(context.Background(), id, ok); err != nil {
		t.Fatalf("valid edges rejected: %v", err)
	}
}

func nan() float64 { var z float64; return z / z }

func TestRetentionChangeTriggersRecompute(t *testing.T) {
	rec := &recorder{}
	svc, _, id := setup(t, WithRetentionRecomputer(rec))
	ctx := context.Background()
	f := func(v float64) *float64 { return &v }

	if _, err := svc.Update(ctx, id, Overrides{DesiredRetention: f(0.85)}); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 || rec.calls[0] != 0.85 {
		t.Fatalf("calls = %v", rec.calls)
	}
	// Unchanged retention, other field changes: no recompute.
	n := 3
	if _, err := svc.Update(ctx, id, Overrides{DesiredRetention: f(0.85), NewPerDay: &n}); err != nil {
		t.Fatal(err)
	}
	// Setting the override equal to the default over a non-default is a change; clearing back is too.
	if _, err := svc.Update(ctx, id, Overrides{}); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 2 || rec.calls[1] != 0.90 {
		t.Fatalf("calls = %v", rec.calls)
	}
	// An explicit override equal to the default is not a change.
	if _, err := svc.Update(ctx, id, Overrides{DesiredRetention: f(0.90)}); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("calls = %v", rec.calls)
	}
}

func TestRetentionRecomputeFailureIsReportedAfterStore(t *testing.T) {
	boom := errors.New("boom")
	rec := &recorder{err: boom}
	svc, repo, id := setup(t, WithRetentionRecomputer(rec))
	r := 0.8
	got, err := svc.Update(context.Background(), id, Overrides{DesiredRetention: &r})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got.DesiredRetention != 0.8 || repo.users[id].DesiredRetention == nil {
		t.Fatalf("setting should be stored: %+v", got)
	}
}

func TestRetentionChangeWithoutHookIsFine(t *testing.T) {
	svc, _, id := setup(t)
	r := 0.8
	if _, err := svc.Update(context.Background(), id, Overrides{DesiredRetention: &r}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceStudyDayUsesUserZoneAndRollover(t *testing.T) {
	svc, _, id := setup(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 10, 7, 30, 0, 0, time.UTC) // 03:30 New York

	sd, err := svc.StudyDay(ctx, id, now)
	if err != nil || sd.Date != "2026-06-09" {
		t.Fatalf("default: %+v err=%v", sd, err)
	}
	tz, roh := "Asia/Tokyo", 4
	if _, err := svc.Update(ctx, id, Overrides{Timezone: &tz, RolloverHour: &roh}); err != nil {
		t.Fatal(err)
	}
	sd, err = svc.StudyDay(ctx, id, now) // 16:30 Tokyo
	if err != nil || sd.Date != "2026-06-10" {
		t.Fatalf("tokyo: %+v err=%v", sd, err)
	}
}
