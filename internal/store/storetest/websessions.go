// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
)

func runWebSessions(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})

	sess := func(uid domain.ID, hash string) auth.WebSession {
		return auth.WebSession{
			ID: ids.New(), UserID: uid, TokenHash: hash, CSRFToken: "csrf-" + hash,
			CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(time.Hour),
		}
	}

	t.Run("create and get by token hash round trip", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "ada")
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		ws := sess(u.ID, "h1")
		if err := s.WebSessions().Create(ctx, ws); err != nil {
			t.Fatal(err)
		}
		got, err := s.WebSessions().GetByTokenHash(ctx, "h1")
		if err != nil {
			t.Fatal(err)
		}
		if got != ws {
			t.Fatalf("got %+v, want %+v", got, ws)
		}
		if _, err := s.WebSessions().GetByTokenHash(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("token hash is unique and user must exist", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "ada")
		_ = s.Users().Create(ctx, u)
		if err := s.WebSessions().Create(ctx, sess(u.ID, "dup")); err != nil {
			t.Fatal(err)
		}
		if err := s.WebSessions().Create(ctx, sess(u.ID, "dup")); err == nil {
			t.Fatal("want unique violation")
		}
		if err := s.WebSessions().Create(ctx, sess(ids.New(), "orphan")); err == nil {
			t.Fatal("want foreign key error")
		}
	})

	t.Run("touch and delete are owner scoped", func(t *testing.T) {
		s, _ := newStore(t)
		u1, u2 := newUser(ids, "a"), newUser(ids, "b")
		_ = s.Users().Create(ctx, u1)
		_ = s.Users().Create(ctx, u2)
		ws := sess(u1.ID, "h1")
		if err := s.WebSessions().Create(ctx, ws); err != nil {
			t.Fatal(err)
		}
		seen, exp := t0.Add(time.Minute), t0.Add(2*time.Hour)

		if err := s.WebSessions().Touch(ctx, u2.ID, ws.ID, seen, exp); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("other user's touch: err = %v, want ErrNotFound", err)
		}
		if err := s.WebSessions().Delete(ctx, u2.ID, ws.ID); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("other user's delete: err = %v, want ErrNotFound", err)
		}
		got, err := s.WebSessions().GetByTokenHash(ctx, "h1")
		if err != nil || !got.LastSeenAt.Equal(t0) || !got.ExpiresAt.Equal(t0.Add(time.Hour)) {
			t.Fatalf("session changed by another user: %+v err=%v", got, err)
		}

		if err := s.WebSessions().Touch(ctx, u1.ID, ws.ID, seen, exp); err != nil {
			t.Fatal(err)
		}
		got, _ = s.WebSessions().GetByTokenHash(ctx, "h1")
		if !got.LastSeenAt.Equal(seen) || !got.ExpiresAt.Equal(exp) {
			t.Fatalf("touch not applied: %+v", got)
		}
		if err := s.WebSessions().Delete(ctx, u1.ID, ws.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.WebSessions().GetByTokenHash(ctx, "h1"); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("after delete: err = %v", err)
		}
		if err := s.WebSessions().Delete(ctx, u1.ID, ws.ID); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("second delete: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("purge removes only expired sessions, for every user", func(t *testing.T) {
		s, _ := newStore(t)
		u1, u2 := newUser(ids, "a"), newUser(ids, "b")
		_ = s.Users().Create(ctx, u1)
		_ = s.Users().Create(ctx, u2)
		old1, old2, live, edge := sess(u1.ID, "old1"), sess(u2.ID, "old2"), sess(u1.ID, "live"), sess(u2.ID, "edge")
		old1.ExpiresAt, old2.ExpiresAt = t0.Add(time.Minute), t0.Add(2*time.Minute)
		edge.ExpiresAt = t0.Add(10 * time.Minute) // expires exactly at now: purged
		live.ExpiresAt = t0.Add(10*time.Minute + time.Millisecond)
		for _, ws := range []auth.WebSession{old1, old2, live, edge} {
			if err := s.WebSessions().Create(ctx, ws); err != nil {
				t.Fatal(err)
			}
		}
		n, err := s.WebSessions().PurgeExpired(ctx, t0.Add(10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Fatalf("purged %d sessions, want 3", n)
		}
		for _, h := range []string{"old1", "old2", "edge"} {
			if _, err := s.WebSessions().GetByTokenHash(ctx, h); !errors.Is(err, auth.ErrNotFound) {
				t.Fatalf("%s: err = %v, want ErrNotFound", h, err)
			}
		}
		if got, err := s.WebSessions().GetByTokenHash(ctx, "live"); err != nil || got != live {
			t.Fatalf("live session: got %+v err=%v", got, err)
		}
		if n, err := s.WebSessions().PurgeExpired(ctx, t0.Add(10*time.Minute)); err != nil || n != 0 {
			t.Fatalf("second purge: n=%d err=%v, want 0", n, err)
		}
	})
}

func runLoginStates(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	st := func(h string, exp time.Time) auth.LoginState {
		return auth.LoginState{StateHash: h, Nonce: "n-" + h, CodeVerifier: "v-" + h, ReturnTo: "/x", ExpiresAt: exp}
	}

	t.Run("consume is single use", func(t *testing.T) {
		s, _ := newStore(t)
		if err := s.LoginStates().Create(ctx, st("a", t0.Add(time.Minute))); err != nil {
			t.Fatal(err)
		}
		got, err := s.LoginStates().Consume(ctx, "a", t0)
		if err != nil || got.Nonce != "n-a" || got.CodeVerifier != "v-a" || got.ReturnTo != "/x" {
			t.Fatalf("got %+v err=%v", got, err)
		}
		if _, err := s.LoginStates().Consume(ctx, "a", t0); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("second consume: err = %v, want ErrNotFound", err)
		}
		if _, err := s.LoginStates().Consume(ctx, "missing", t0); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("unknown consume: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("expired state is rejected and removed", func(t *testing.T) {
		s, _ := newStore(t)
		_ = s.LoginStates().Create(ctx, st("a", t0.Add(time.Minute)))
		if _, err := s.LoginStates().Consume(ctx, "a", t0.Add(time.Minute)); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound at expiry", err)
		}
		if _, err := s.LoginStates().Consume(ctx, "a", t0); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("expired state should have been removed: err = %v", err)
		}
	})

	t.Run("purge removes only expired states", func(t *testing.T) {
		s, _ := newStore(t)
		_ = s.LoginStates().Create(ctx, st("old", t0.Add(time.Minute)))
		_ = s.LoginStates().Create(ctx, st("new", t0.Add(time.Hour)))
		n, err := s.LoginStates().PurgeExpired(ctx, t0.Add(10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("purged %d states, want 1", n)
		}
		if _, err := s.LoginStates().Consume(ctx, "old", t0); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("old: err = %v", err)
		}
		if _, err := s.LoginStates().Consume(ctx, "new", t0); err != nil {
			t.Fatalf("new: %v", err)
		}
	})
}
