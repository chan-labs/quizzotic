// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/store"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func newUser(ids *domain.IDGenerator, name string) auth.User {
	return auth.User{ID: ids.New(), DisplayName: name, Email: name + "@example.com", CreatedAt: t0}
}

func runUsers(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})

	t.Run("create and get round trip", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "ada")
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		got, err := s.Users().Get(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != u.ID || got.DisplayName != "ada" || got.Email != "ada@example.com" || !got.CreatedAt.Equal(t0) {
			t.Fatalf("got %+v", got)
		}
		if got.Scheduling.DesiredRetention != nil || got.Scheduling.LearningSteps != nil || got.Scheduling.Timezone != nil {
			t.Fatalf("scheduling overrides should default to nil: %+v", got.Scheduling)
		}
	})

	t.Run("scheduling settings round trip", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "grace")
		ret, n, tz := 0.85, 7, "Europe/London"
		u.Scheduling = auth.Scheduling{DesiredRetention: &ret, LearningSteps: []string{"2m", "15m"}, NewPerDay: &n, Timezone: &tz}
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		got, err := s.Users().Get(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		sc := got.Scheduling
		if sc.DesiredRetention == nil || *sc.DesiredRetention != 0.85 || sc.NewPerDay == nil || *sc.NewPerDay != 7 ||
			sc.Timezone == nil || *sc.Timezone != tz || len(sc.LearningSteps) != 2 || sc.LearningSteps[1] != "15m" ||
			sc.MaxReviewsPerDay != nil {
			t.Fatalf("scheduling = %+v", sc)
		}
	})

	t.Run("get missing", func(t *testing.T) {
		s, _ := newStore(t)
		if _, err := s.Users().Get(ctx, ids.New()); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("update profile", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "ada")
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		if err := s.Users().UpdateProfile(ctx, u.ID, "Ada L", "ada@example.org"); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Users().Get(ctx, u.ID)
		if got.DisplayName != "Ada L" || got.Email != "ada@example.org" {
			t.Fatalf("got %+v", got)
		}
		if err := s.Users().UpdateProfile(ctx, ids.New(), "x", "y"); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("update missing: err = %v", err)
		}
	})
}

func runIdentities(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})
	const iss = "https://idp.example.com"

	ident := func(uid domain.ID, issuer, sub string) auth.Identity {
		return auth.Identity{ID: ids.New(), UserID: uid, Issuer: issuer, Subject: sub, CreatedAt: t0, LastSeenAt: t0}
	}

	t.Run("insert, lookup, unique on issuer and subject", func(t *testing.T) {
		s, _ := newStore(t)
		u1, u2 := newUser(ids, "a"), newUser(ids, "b")
		for _, u := range []auth.User{u1, u2} {
			if err := s.Users().Create(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
		ok, err := s.Identities().InsertIfAbsent(ctx, ident(u1.ID, iss, "sub-1"))
		if err != nil || !ok {
			t.Fatalf("first insert ok=%v err=%v", ok, err)
		}
		ok, err = s.Identities().InsertIfAbsent(ctx, ident(u2.ID, iss, "sub-1"))
		if err != nil || ok {
			t.Fatalf("duplicate insert ok=%v err=%v, want false,nil", ok, err)
		}
		got, err := s.Identities().GetByIssuerSubject(ctx, iss, "sub-1")
		if err != nil || got.UserID != u1.ID {
			t.Fatalf("got %+v err=%v, want user %s", got, err, u1.ID)
		}
		// Same subject under another issuer is a different identity.
		if ok, err := s.Identities().InsertIfAbsent(ctx, ident(u2.ID, "https://other.example.com", "sub-1")); err != nil || !ok {
			t.Fatalf("other issuer ok=%v err=%v", ok, err)
		}
		if _, err := s.Identities().GetByIssuerSubject(ctx, iss, "nope"); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("identity needs an existing user", func(t *testing.T) {
		s, _ := newStore(t)
		if _, err := s.Identities().InsertIfAbsent(ctx, ident(ids.New(), iss, "x")); err == nil {
			t.Fatal("want foreign key error")
		}
	})

	t.Run("touch last seen is owner scoped", func(t *testing.T) {
		s, _ := newStore(t)
		u1, u2 := newUser(ids, "a"), newUser(ids, "b")
		_ = s.Users().Create(ctx, u1)
		_ = s.Users().Create(ctx, u2)
		i := ident(u1.ID, iss, "sub-1")
		if _, err := s.Identities().InsertIfAbsent(ctx, i); err != nil {
			t.Fatal(err)
		}
		later := t0.Add(time.Hour)
		if err := s.Identities().TouchLastSeen(ctx, u2.ID, i.ID, later); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("other user's touch: err = %v, want ErrNotFound", err)
		}
		if err := s.Identities().TouchLastSeen(ctx, u1.ID, i.ID, later); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Identities().GetByIssuerSubject(ctx, iss, "sub-1")
		if !got.LastSeenAt.Equal(later) {
			t.Fatalf("last_seen_at = %v, want %v", got.LastSeenAt, later)
		}
	})
}

func runProvision(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	const iss = "https://idp.example.com"

	svc := func(s store.Store, clock domain.Clock) *auth.Service {
		return auth.NewService(store.AuthTx(s), clock, domain.NewIDGenerator(clock))
	}

	t.Run("first login creates user and identity from claims", func(t *testing.T) {
		s, sc := newStore(t)
		clock := domain.NewFakeClock(t0)
		u, err := svc(s, clock).Provision(ctx, auth.Claims{Issuer: iss, Subject: "s1", DisplayName: "Ada", Email: "ada@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if u.DisplayName != "Ada" || u.Email != "ada@example.com" || !u.CreatedAt.Equal(t0) {
			t.Fatalf("user = %+v", u)
		}
		i, err := s.Identities().GetByIssuerSubject(ctx, iss, "s1")
		if err != nil || i.UserID != u.ID || !i.LastSeenAt.Equal(t0) {
			t.Fatalf("identity = %+v err=%v", i, err)
		}
		if n, _ := sc.CountUsers(ctx, s); n != 1 {
			t.Fatalf("users = %d", n)
		}
	})

	t.Run("later login updates last seen and claims", func(t *testing.T) {
		s, _ := newStore(t)
		clock := domain.NewFakeClock(t0)
		sv := svc(s, clock)
		first, err := sv.Provision(ctx, auth.Claims{Issuer: iss, Subject: "s1", DisplayName: "Ada", Email: "ada@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(48 * time.Hour)
		again, err := sv.Provision(ctx, auth.Claims{Issuer: iss, Subject: "s1", DisplayName: "Ada L", Email: ""})
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != first.ID || again.DisplayName != "Ada L" || again.Email != "ada@example.com" {
			t.Fatalf("again = %+v", again)
		}
		i, _ := s.Identities().GetByIssuerSubject(ctx, iss, "s1")
		if !i.LastSeenAt.Equal(clock.Now()) || !i.CreatedAt.Equal(t0) {
			t.Fatalf("identity = %+v", i)
		}
	})

	t.Run("distinct subjects and issuers get distinct users", func(t *testing.T) {
		s, sc := newStore(t)
		sv := svc(s, domain.SystemClock{})
		seen := map[domain.ID]bool{}
		for _, c := range []auth.Claims{{Issuer: iss, Subject: "a"}, {Issuer: iss, Subject: "b"}, {Issuer: "https://o.example.com", Subject: "a"}} {
			u, err := sv.Provision(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			seen[u.ID] = true
		}
		if n, _ := sc.CountUsers(ctx, s); len(seen) != 3 || n != 3 {
			t.Fatalf("distinct=%d users=%d, want 3", len(seen), n)
		}
	})

	t.Run("rejects empty issuer or subject", func(t *testing.T) {
		s, _ := newStore(t)
		sv := svc(s, domain.SystemClock{})
		if _, err := sv.Provision(ctx, auth.Claims{Issuer: iss}); err == nil {
			t.Fatal("want error for empty subject")
		}
		if _, err := sv.Provision(ctx, auth.Claims{Subject: "x"}); err == nil {
			t.Fatal("want error for empty issuer")
		}
	})

	t.Run("concurrent first logins yield one user per issuer and subject", func(t *testing.T) {
		s, sc := newStore(t)
		sv := svc(s, domain.SystemClock{})
		const subjects, perSubject = 3, 12

		type result struct {
			sub string
			id  domain.ID
			err error
		}
		var wg sync.WaitGroup
		out := make(chan result, subjects*perSubject)
		start := make(chan struct{})
		for si := 0; si < subjects; si++ {
			for k := 0; k < perSubject; k++ {
				wg.Add(1)
				go func(sub string) {
					defer wg.Done()
					<-start
					u, err := sv.Provision(ctx, auth.Claims{Issuer: iss, Subject: sub, DisplayName: sub})
					out <- result{sub, u.ID, err}
				}(fmt.Sprintf("sub-%d", si))
			}
		}
		close(start)
		wg.Wait()
		close(out)

		bySub := map[string]domain.ID{}
		for r := range out {
			if r.err != nil {
				t.Fatalf("provision %s: %v", r.sub, r.err)
			}
			if prev, ok := bySub[r.sub]; ok && prev != r.id {
				t.Fatalf("%s resolved to two users: %s and %s", r.sub, prev, r.id)
			}
			bySub[r.sub] = r.id
		}
		if len(bySub) != subjects {
			t.Fatalf("subjects resolved = %d, want %d", len(bySub), subjects)
		}
		if n, _ := sc.CountUsers(ctx, s); n != subjects {
			t.Fatalf("users = %d, want %d (orphans or duplicates)", n, subjects)
		}
		if n, _ := sc.CountIdentities(ctx, s); n != subjects {
			t.Fatalf("identities = %d, want %d", n, subjects)
		}
	})
}
