// SPDX-License-Identifier: Apache-2.0

package problems_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// TestTransitionTable covers every ordered pair of statuses, including the
// same-status pairs.
func TestTransitionTable(t *testing.T) {
	d, l, a := problems.StatusDraft, problems.StatusLive, problems.StatusArchived
	cases := []struct {
		from, to problems.Status
		want     bool
	}{
		{d, d, false}, {d, l, true}, {d, a, false},
		{l, d, false}, {l, l, false}, {l, a, true},
		{a, d, false}, {a, l, false}, {a, a, false},
	}
	for _, c := range cases {
		if got := problems.CanTransition(c.from, c.to); got != c.want {
			t.Errorf("CanTransition(%s, %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

// problemIn creates a problem and drives it to status through the service.
func problemIn(t *testing.T, f fixture, status problems.Status) problems.View {
	t.Helper()
	ctx := context.Background()
	v, err := f.svc.Create(ctx, f.actor, mcqInput(false))
	if err != nil {
		t.Fatal(err)
	}
	if status != problems.StatusDraft {
		f.clock.Advance(time.Second)
		if v, err = f.svc.Publish(ctx, f.actor.UserID, v.ID, v.ETag()); err != nil {
			t.Fatal(err)
		}
	}
	if status == problems.StatusArchived {
		f.clock.Advance(time.Second)
		if v, err = f.svc.Archive(ctx, f.actor.UserID, v.ID, v.ETag()); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

// TestServiceTransitionsMatchTable runs both lifecycle actions from every
// status and checks the outcome against the table.
func TestServiceTransitionsMatchTable(t *testing.T) {
	ctx := context.Background()
	actions := []struct {
		name string
		to   problems.Status
		do   func(f fixture, id domain.ID, etag string) (problems.View, error)
	}{
		{"publish", problems.StatusLive, func(f fixture, id domain.ID, etag string) (problems.View, error) {
			return f.svc.Publish(ctx, f.actor.UserID, id, etag)
		}},
		{"archive", problems.StatusArchived, func(f fixture, id domain.ID, etag string) (problems.View, error) {
			return f.svc.Archive(ctx, f.actor.UserID, id, etag)
		}},
	}
	for _, from := range []problems.Status{problems.StatusDraft, problems.StatusLive, problems.StatusArchived} {
		for _, act := range actions {
			t.Run(string(from)+"/"+act.name, func(t *testing.T) {
				f := newFixture(t)
				v := problemIn(t, f, from)
				f.clock.Advance(time.Second)
				got, err := act.do(f, v.ID, v.ETag())
				if problems.CanTransition(from, act.to) {
					if err != nil || got.Status != act.to {
						t.Fatalf("got %+v, %v", got, err)
					}
					if got.ETag() == v.ETag() {
						t.Error("ETag unchanged by a status change")
					}
					return
				}
				var pb *platform.Problem
				if !errors.As(err, &pb) || pb.Status != http.StatusConflict {
					t.Fatalf("err = %v, want 409", err)
				}
				cur, _ := f.svc.Get(ctx, f.actor.UserID, v.ID)
				if cur.Status != from {
					t.Errorf("status changed to %s despite 409", cur.Status)
				}
			})
		}
	}
}

func TestLifecycleIfMatchAndDelete(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	v := problemIn(t, f, problems.StatusDraft)
	var pb *platform.Problem
	if _, err := f.svc.Publish(ctx, f.actor.UserID, v.ID, `"stale"`); !errors.As(err, &pb) || pb.Status != http.StatusPreconditionFailed {
		t.Errorf("stale publish err = %v, want 412", err)
	}
	if err := f.svc.Delete(ctx, f.actor.UserID, v.ID, `"stale"`); !errors.As(err, &pb) || pb.Status != http.StatusPreconditionFailed {
		t.Errorf("stale delete err = %v, want 412", err)
	}
	f.clock.Advance(time.Second)
	if err := f.svc.Delete(ctx, f.actor.UserID, v.ID, v.ETag()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(ctx, f.actor.UserID, v.ID); !errors.As(err, &pb) || pb.Status != http.StatusNotFound {
		t.Errorf("get after delete err = %v, want 404", err)
	}
	if err := f.svc.Delete(ctx, f.actor.UserID, v.ID, "*"); !errors.As(err, &pb) || pb.Status != http.StatusNotFound {
		t.Errorf("second delete err = %v, want 404", err)
	}
}
