// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

func runIdempotency(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})

	rec := func(uid domain.ID, key, hash string, at time.Time) platform.IdempotencyRecord {
		return platform.IdempotencyRecord{
			UserID: uid, Key: key, RequestHash: hash, Status: 201,
			Header:    http.Header{"Content-Type": {"application/json"}, "Location": {"/api/v1/problems/1"}},
			Body:      []byte(`{"id":"1"}`),
			CreatedAt: at, ExpiresAt: at.Add(platform.IdempotencyTTL),
		}
	}

	t.Run("put and get round trip", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "ada")
		_ = s.Users().Create(ctx, u)
		want := rec(u.ID, "k1", "h1", t0)
		if ok, err := s.Idempotency().Put(ctx, want); err != nil || !ok {
			t.Fatalf("put = %v, %v", ok, err)
		}
		got, err := s.Idempotency().Get(ctx, u.ID, "k1", t0.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
		if _, err := s.Idempotency().Get(ctx, u.ID, "other", t0); !errors.Is(err, platform.ErrIdempotencyNotFound) {
			t.Fatalf("unknown key: err = %v", err)
		}
	})

	t.Run("keys are scoped per user", func(t *testing.T) {
		s, _ := newStore(t)
		u1, u2 := newUser(ids, "a"), newUser(ids, "b")
		_ = s.Users().Create(ctx, u1)
		_ = s.Users().Create(ctx, u2)
		if _, err := s.Idempotency().Put(ctx, rec(u1.ID, "same", "h1", t0)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Idempotency().Get(ctx, u2.ID, "same", t0); !errors.Is(err, platform.ErrIdempotencyNotFound) {
			t.Fatalf("other user's get: err = %v, want not found", err)
		}
		// The same key for another user is an independent record.
		if ok, err := s.Idempotency().Put(ctx, rec(u2.ID, "same", "h2", t0)); err != nil || !ok {
			t.Fatalf("other user's put = %v, %v", ok, err)
		}
		got, _ := s.Idempotency().Get(ctx, u1.ID, "same", t0)
		if got.RequestHash != "h1" {
			t.Fatalf("u1 record overwritten: %+v", got)
		}
	})

	t.Run("first put wins while unexpired", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "ada")
		_ = s.Users().Create(ctx, u)
		_, _ = s.Idempotency().Put(ctx, rec(u.ID, "k", "first", t0))
		ok, err := s.Idempotency().Put(ctx, rec(u.ID, "k", "second", t0.Add(time.Hour)))
		if err != nil || ok {
			t.Fatalf("second put = %v, %v; want false, nil", ok, err)
		}
		got, _ := s.Idempotency().Get(ctx, u.ID, "k", t0.Add(time.Hour))
		if got.RequestHash != "first" {
			t.Fatalf("record replaced: %+v", got)
		}
	})

	t.Run("expired record is invisible and replaceable", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "ada")
		_ = s.Users().Create(ctx, u)
		_, _ = s.Idempotency().Put(ctx, rec(u.ID, "k", "old", t0))
		expiry := t0.Add(platform.IdempotencyTTL)
		if _, err := s.Idempotency().Get(ctx, u.ID, "k", expiry.Add(-time.Second)); err != nil {
			t.Fatalf("just before expiry: %v", err)
		}
		if _, err := s.Idempotency().Get(ctx, u.ID, "k", expiry); !errors.Is(err, platform.ErrIdempotencyNotFound) {
			t.Fatalf("at expiry: err = %v, want not found", err)
		}
		ok, err := s.Idempotency().Put(ctx, rec(u.ID, "k", "new", expiry))
		if err != nil || !ok {
			t.Fatalf("put over expired = %v, %v", ok, err)
		}
		got, err := s.Idempotency().Get(ctx, u.ID, "k", expiry)
		if err != nil || got.RequestHash != "new" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("purge removes only expired records", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "ada")
		_ = s.Users().Create(ctx, u)
		_, _ = s.Idempotency().Put(ctx, rec(u.ID, "old", "h", t0))
		_, _ = s.Idempotency().Put(ctx, rec(u.ID, "new", "h", t0.Add(12*time.Hour)))
		now := t0.Add(platform.IdempotencyTTL + time.Hour)
		n, err := s.Idempotency().PurgeExpired(ctx, now)
		if err != nil || n != 1 {
			t.Fatalf("purge = %d, %v; want 1", n, err)
		}
		if _, err := s.Idempotency().Get(ctx, u.ID, "new", now); err != nil {
			t.Fatalf("unexpired record purged: %v", err)
		}
		if n, _ := s.Idempotency().PurgeExpired(ctx, now); n != 0 {
			t.Fatalf("second purge = %d, want 0", n)
		}
	})

	t.Run("user must exist", func(t *testing.T) {
		s, _ := newStore(t)
		if _, err := s.Idempotency().Put(ctx, rec(ids.New(), "k", "h", t0)); err == nil {
			t.Fatal("want foreign key error")
		}
	})
}
