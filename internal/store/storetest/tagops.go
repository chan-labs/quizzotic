// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func runTagOperations(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})

	setup := func(t *testing.T) (store.Store, auth.User, auth.User) {
		t.Helper()
		s, _ := newStore(t)
		a, b := newUser(ids, "a"), newUser(ids, "b")
		for _, u := range []auth.User{a, b} {
			if err := s.Users().Create(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
		return s, a, b
	}
	mkTag := func(t *testing.T, s store.Store, owner auth.User, slug string) tags.Tag {
		t.Helper()
		tg := tags.Tag{ID: ids.New(), OwnerID: owner.ID, Slug: slug, Name: "Name " + slug, Description: "d",
			Status: tags.StatusActive, CreatedAt: t0, UpdatedAt: t0.Add(1500 * time.Millisecond)}
		if err := s.Tags().Create(ctx, owner.ID, tg); err != nil {
			t.Fatal(err)
		}
		return tg
	}
	mkProblem := func(t *testing.T, s store.Store, owner auth.User, tagIDs ...domain.ID) domain.ID {
		t.Helper()
		act := domain.Actor{Kind: domain.ActorHuman, UserID: owner.ID}
		id := ids.New()
		p := problems.Problem{ID: id, OwnerID: owner.ID, Status: problems.StatusLive, CurrentVersion: 1,
			CreatedBy: act, CreatedAt: t0, UpdatedAt: t0}
		v := problems.Version{ProblemID: id, Version: 1, Type: problems.TypeOpen, Prompt: "q",
			Payload: json.RawMessage(`{"reference_answer":"a"}`), ChangeKind: problems.ChangeMajor, CreatedBy: act, CreatedAt: t0}
		if err := s.Problems().Create(ctx, owner.ID, p, v); err != nil {
			t.Fatal(err)
		}
		if err := s.Tags().SetProblemTags(ctx, owner.ID, id, tagIDs); err != nil {
			t.Fatal(err)
		}
		return id
	}
	is := func(t *testing.T, err, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	}
	human := func(u auth.User) domain.Actor { return domain.Actor{Kind: domain.ActorHuman, UserID: u.ID} }
	agent := func(u auth.User) domain.Actor {
		return domain.Actor{Kind: domain.ActorAgent, UserID: u.ID, ClientID: "agent-client"}
	}
	proposed := func(owner auth.User, kind tags.OpKind, payload string) tags.Operation {
		return tags.Operation{ID: ids.New(), OwnerID: owner.ID, Kind: kind, Payload: json.RawMessage(payload),
			Status: tags.OpProposed, ProposedBy: agent(owner), CreatedAt: t0}
	}

	t.Run("snapshot of tags and mappings reads back exactly", func(t *testing.T) {
		s, a, _ := setup(t)
		src, dst, other := mkTag(t, s, a, "src"), mkTag(t, s, a, "dst"), mkTag(t, s, a, "other")
		// src gains an alias by rename, dst is merged-into style data.
		src.Slug, src.UpdatedAt = "renamed", t0.Add(time.Hour)
		if err := s.Tags().Update(ctx, a.ID, src); err != nil {
			t.Fatal(err)
		}
		p1 := mkProblem(t, s, a, src.ID, other.ID)
		p2 := mkProblem(t, s, a, src.ID, dst.ID)
		mkProblem(t, s, a, other.ID) // unrelated to the snapshot's tags

		snap, err := s.TagOperations().CaptureSnapshot(ctx, a.ID, []domain.ID{dst.ID, src.ID, src.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Tags) != 2 || len(snap.Mappings) != 3 {
			t.Fatalf("snapshot = %+v", snap)
		}
		got := map[domain.ID]tags.SnapshotTag{}
		for _, st := range snap.Tags {
			got[st.ID] = st
		}
		stored, err := s.Tags().Get(ctx, a.ID, src.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got[src.ID], tags.SnapshotTagOf(stored)) || !reflect.DeepEqual(got[src.ID].Aliases, []string{"src"}) ||
			got[dst.ID].Slug != "dst" || !got[src.ID].UpdatedAt.Equal(t0.Add(time.Hour)) {
			t.Fatalf("tags in snapshot = %+v", got)
		}
		want := map[tags.Mapping]bool{{ProblemID: p1, TagID: src.ID}: true, {ProblemID: p2, TagID: src.ID}: true, {ProblemID: p2, TagID: dst.ID}: true}
		for _, m := range snap.Mappings {
			if !want[m] {
				t.Fatalf("unexpected mapping %+v", m)
			}
			delete(want, m)
		}
		if len(want) != 0 {
			t.Fatalf("missing mappings %+v", want)
		}

		// Store it on an applied operation and read it back exactly.
		op := proposed(a, tags.OpMerge, `{"sources":["`+src.ID.String()+`"],"target":"`+dst.ID.String()+`"}`)
		if err := s.TagOperations().Create(ctx, a.ID, op); err != nil {
			t.Fatal(err)
		}
		dec := tags.Decision{Status: tags.OpApplied, DecidedBy: human(a), DecidedAt: t0.Add(2 * time.Minute),
			AppliedAt: t0.Add(2 * time.Minute), Snapshot: &snap}
		if err := s.TagOperations().Decide(ctx, a.ID, op.ID, dec); err != nil {
			t.Fatal(err)
		}
		back, err := s.TagOperations().Get(ctx, a.ID, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if back.Snapshot == nil || !reflect.DeepEqual(*back.Snapshot, snap) {
			t.Fatalf("snapshot round trip:\n got %+v\nwant %+v", back.Snapshot, snap)
		}
		if back.Status != tags.OpApplied || back.DecidedBy != human(a) || !back.AppliedAt.Equal(dec.AppliedAt) ||
			!back.DecidedAt.Equal(dec.DecidedAt) || back.ProposedBy != agent(a) || back.Kind != tags.OpMerge ||
			string(back.Payload) != string(op.Payload) || !back.CreatedAt.Equal(t0) {
			t.Fatalf("operation = %+v", back)
		}
	})

	t.Run("empty snapshot and foreign tag", func(t *testing.T) {
		s, a, b := setup(t)
		snap, err := s.TagOperations().CaptureSnapshot(ctx, a.ID, nil)
		if err != nil || snap.Tags == nil || snap.Mappings == nil || len(snap.Tags)+len(snap.Mappings) != 0 {
			t.Fatalf("empty = %+v, %v", snap, err)
		}
		bt := mkTag(t, s, b, "b")
		_, err = s.TagOperations().CaptureSnapshot(ctx, a.ID, []domain.ID{bt.ID})
		is(t, err, tags.ErrNotFound)
	})

	t.Run("proposed then rejected", func(t *testing.T) {
		s, a, _ := setup(t)
		op := proposed(a, tags.OpSplit, `{}`)
		if err := s.TagOperations().Create(ctx, a.ID, op); err != nil {
			t.Fatal(err)
		}
		got, err := s.TagOperations().Get(ctx, a.ID, op.ID)
		if err != nil || got.Status != tags.OpProposed || got.Snapshot != nil || !got.AppliedAt.IsZero() ||
			!got.DecidedAt.IsZero() || got.DecidedBy != (domain.Actor{}) || got.ProposedBy != agent(a) {
			t.Fatalf("proposed = %+v, %v", got, err)
		}
		if err := s.TagOperations().Decide(ctx, a.ID, op.ID, tags.Decision{Status: tags.OpRejected,
			DecidedBy: human(a), DecidedAt: t0.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		got, err = s.TagOperations().Get(ctx, a.ID, op.ID)
		if err != nil || got.Status != tags.OpRejected || got.Snapshot != nil || !got.AppliedAt.IsZero() ||
			got.DecidedBy != human(a) || !got.DecidedAt.Equal(t0.Add(time.Minute)) {
			t.Fatalf("rejected = %+v, %v", got, err)
		}
	})

	t.Run("decided operations are final and snapshots are never rewritten", func(t *testing.T) {
		s, a, _ := setup(t)
		tg := mkTag(t, s, a, "x")
		snap, err := s.TagOperations().CaptureSnapshot(ctx, a.ID, []domain.ID{tg.ID})
		if err != nil {
			t.Fatal(err)
		}
		op := proposed(a, tags.OpRename, `{}`)
		if err := s.TagOperations().Create(ctx, a.ID, op); err != nil {
			t.Fatal(err)
		}
		apply := tags.Decision{Status: tags.OpApplied, DecidedBy: human(a), DecidedAt: t0, AppliedAt: t0, Snapshot: &snap}
		if err := s.TagOperations().Decide(ctx, a.ID, op.ID, apply); err != nil {
			t.Fatal(err)
		}
		is(t, s.TagOperations().Decide(ctx, a.ID, op.ID, apply), tags.ErrOpNotProposed)
		is(t, s.TagOperations().Decide(ctx, a.ID, op.ID, tags.Decision{Status: tags.OpRejected, DecidedBy: human(a), DecidedAt: t0}), tags.ErrOpNotProposed)
		is(t, s.TagOperations().Decide(ctx, a.ID, ids.New(), apply), tags.ErrOpNotFound)
		got, err := s.TagOperations().Get(ctx, a.ID, op.ID)
		if err != nil || got.Status != tags.OpApplied || !reflect.DeepEqual(*got.Snapshot, snap) {
			t.Fatalf("after refused decisions = %+v, %v", got, err)
		}
		if err := s.TagOperations().Decide(ctx, a.ID, ids.New(), tags.Decision{Status: tags.OpProposed}); err == nil {
			t.Fatal("want error deciding to proposed")
		}
	})

	t.Run("created already applied, list and filter", func(t *testing.T) {
		s, a, _ := setup(t)
		snap := tags.Snapshot{}
		direct := proposed(a, tags.OpRename, `{"slug":"n"}`)
		direct.Status, direct.DecidedBy, direct.DecidedAt, direct.AppliedAt, direct.Snapshot =
			tags.OpApplied, human(a), t0, t0, &snap
		if err := s.TagOperations().Create(ctx, a.ID, direct); err != nil {
			t.Fatal(err)
		}
		pending := proposed(a, tags.OpMerge, `{}`)
		if err := s.TagOperations().Create(ctx, a.ID, pending); err != nil {
			t.Fatal(err)
		}
		all, err := s.TagOperations().List(ctx, a.ID, tags.OpFilter{})
		if err != nil || len(all) != 2 || all[0].ID != direct.ID || all[1].ID != pending.ID {
			t.Fatalf("list = %+v, %v", all, err)
		}
		if all[0].Snapshot == nil || all[0].Snapshot.Tags == nil || all[0].Snapshot.Mappings == nil {
			t.Fatalf("empty snapshot should read back as empty, non-nil slices: %+v", all[0].Snapshot)
		}
		onlyP, err := s.TagOperations().List(ctx, a.ID, tags.OpFilter{Status: tags.OpProposed})
		if err != nil || len(onlyP) != 1 || onlyP[0].ID != pending.ID {
			t.Fatalf("proposed list = %+v, %v", onlyP, err)
		}
		page, err := s.TagOperations().List(ctx, a.ID, tags.OpFilter{After: direct.ID, Limit: 1})
		if err != nil || len(page) != 1 || page[0].ID != pending.ID {
			t.Fatalf("page = %+v, %v", page, err)
		}
	})

	t.Run("invalid rows are refused", func(t *testing.T) {
		s, a, b := setup(t)
		bad := proposed(a, "bogus", `{}`)
		if err := s.TagOperations().Create(ctx, a.ID, bad); err == nil {
			t.Fatal("want error for unknown kind")
		}
		foreign := proposed(b, tags.OpMerge, `{}`)
		if err := s.TagOperations().Create(ctx, a.ID, foreign); err == nil {
			t.Fatal("want error for owner mismatch")
		}
		noSnap := proposed(a, tags.OpMerge, `{}`)
		noSnap.Status, noSnap.DecidedBy, noSnap.DecidedAt, noSnap.AppliedAt = tags.OpApplied, human(a), t0, t0
		if err := s.TagOperations().Create(ctx, a.ID, noSnap); err == nil {
			t.Fatal("want error for applied without snapshot")
		}
	})

	t.Run("owner scoping", func(t *testing.T) {
		s, a, b := setup(t)
		op := proposed(a, tags.OpMerge, `{}`)
		if err := s.TagOperations().Create(ctx, a.ID, op); err != nil {
			t.Fatal(err)
		}
		_, err := s.TagOperations().Get(ctx, b.ID, op.ID)
		is(t, err, tags.ErrOpNotFound)
		is(t, s.TagOperations().Decide(ctx, b.ID, op.ID, tags.Decision{Status: tags.OpRejected, DecidedBy: human(b), DecidedAt: t0}), tags.ErrOpNotFound)
		list, err := s.TagOperations().List(ctx, b.ID, tags.OpFilter{})
		if err != nil || len(list) != 0 {
			t.Fatalf("b list = %+v, %v", list, err)
		}
		got, err := s.TagOperations().Get(ctx, a.ID, op.ID)
		if err != nil || got.Status != tags.OpProposed {
			t.Fatalf("a's op after b's attempts = %+v, %v", got, err)
		}
	})
}
