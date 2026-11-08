// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// memIdem is an in-memory IdempotencyRepo with the same semantics as the
// store backends (which are covered by the storetest conformance suite).
type memIdem struct {
	mu sync.Mutex
	m  map[string]IdempotencyRecord
}

func newMemIdem() *memIdem { return &memIdem{m: map[string]IdempotencyRecord{}} }

func (r *memIdem) k(u domain.ID, key string) string { return u.String() + "/" + key }

func (r *memIdem) Get(_ context.Context, u domain.ID, key string, now time.Time) (IdempotencyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.m[r.k(u, key)]
	if !ok || !now.Before(rec.ExpiresAt) {
		return IdempotencyRecord{}, ErrIdempotencyNotFound
	}
	return rec, nil
}

func (r *memIdem) Put(_ context.Context, rec IdempotencyRecord) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.m[r.k(rec.UserID, rec.Key)]; ok && old.ExpiresAt.After(rec.CreatedAt) {
		return false, nil
	}
	r.m[r.k(rec.UserID, rec.Key)] = rec
	return true, nil
}

func (r *memIdem) PurgeExpired(_ context.Context, now time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for k, rec := range r.m {
		if !now.Before(rec.ExpiresAt) {
			delete(r.m, k)
			n++
		}
	}
	return n, nil
}

type userCtxKey struct{}

type idemFixture struct {
	clock *domain.FakeClock
	repo  *memIdem
	calls atomic.Int32
	h     http.Handler
	ids   *domain.IDGenerator
}

func newIdemFixture(t *testing.T, status int) *idemFixture {
	t.Helper()
	f := &idemFixture{
		clock: domain.NewFakeClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		repo:  newMemIdem(),
		ids:   domain.NewIDGenerator(domain.SystemClock{}),
	}
	userID := func(ctx context.Context) (domain.ID, bool) {
		id, ok := ctx.Value(userCtxKey{}).(domain.ID)
		return id, ok
	}
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/api/v1/things/1")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"call":` + string('0'+n) + `}`))
	})
	f.h = Idempotency(f.repo, f.clock, userID)(app)
	return f
}

func (f *idemFixture) do(user domain.ID, method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set(IdempotencyKeyHeader, key)
	}
	if !user.IsNil() {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey{}, user))
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func TestIdempotencyReplay(t *testing.T) {
	f := newIdemFixture(t, http.StatusCreated)
	u := f.ids.New()

	first := f.do(u, "POST", "/things", "k1", `{"a":1}`)
	if first.Code != 201 || first.Header().Get(IdempotencyReplayedHeader) != "" {
		t.Fatalf("first: %d replayed=%q", first.Code, first.Header().Get(IdempotencyReplayedHeader))
	}

	f.clock.Advance(23 * time.Hour)
	again := f.do(u, "POST", "/things", "k1", `{"a":1}`)
	if f.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", f.calls.Load())
	}
	if again.Code != 201 || again.Body.String() != first.Body.String() {
		t.Fatalf("replay: %d %q, want %d %q", again.Code, again.Body, first.Code, first.Body)
	}
	if again.Header().Get(IdempotencyReplayedHeader) != "true" ||
		again.Header().Get("Location") != "/api/v1/things/1" ||
		again.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replay headers: %v", again.Header())
	}
}

func TestIdempotencyMismatch(t *testing.T) {
	f := newIdemFixture(t, http.StatusCreated)
	u := f.ids.New()
	f.do(u, "POST", "/things", "k1", `{"a":1}`)

	for name, tc := range map[string]struct{ path, body string }{
		"different body": {"/things", `{"a":2}`},
		"different path": {"/other", `{"a":1}`},
	} {
		w := f.do(u, "POST", tc.path, "k1", tc.body)
		var p Problem
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		if w.Code != 422 || p.Type != TypeIdempotencyKeyReuse || w.Header().Get("Content-Type") != ProblemContentType {
			t.Fatalf("%s: %d %+v", name, w.Code, p)
		}
	}
	if f.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", f.calls.Load())
	}
}

func TestIdempotencyExpiry(t *testing.T) {
	f := newIdemFixture(t, http.StatusCreated)
	u := f.ids.New()
	f.do(u, "POST", "/things", "k1", `{"a":1}`)

	f.clock.Advance(IdempotencyTTL)
	// After 24 h the key is free again: a different body is not a mismatch
	// and the same body runs the handler a second time.
	w := f.do(u, "POST", "/things", "k1", `{"a":2}`)
	if w.Code != 201 || w.Header().Get(IdempotencyReplayedHeader) != "" || f.calls.Load() != 2 {
		t.Fatalf("after expiry: %d replayed=%q calls=%d", w.Code, w.Header().Get(IdempotencyReplayedHeader), f.calls.Load())
	}
	// The new response is stored and replays.
	w = f.do(u, "POST", "/things", "k1", `{"a":2}`)
	if w.Header().Get(IdempotencyReplayedHeader) != "true" || f.calls.Load() != 2 {
		t.Fatalf("replay of re-stored: %v calls=%d", w.Header(), f.calls.Load())
	}
}

func TestIdempotencySweep(t *testing.T) {
	f := newIdemFixture(t, http.StatusCreated)
	u := f.ids.New()
	f.do(u, "POST", "/things", "k1", `{}`)
	f.clock.Advance(IdempotencyTTL + time.Minute)
	f.do(u, "POST", "/things", "k2", `{}`)

	n, err := f.repo.PurgeExpired(context.Background(), f.clock.Now())
	if err != nil || n != 1 || len(f.repo.m) != 1 {
		t.Fatalf("purged %d (%v), %d left", n, err, len(f.repo.m))
	}
}

func TestIdempotencyKeyIsPerUser(t *testing.T) {
	f := newIdemFixture(t, http.StatusCreated)
	u1, u2 := f.ids.New(), f.ids.New()
	f.do(u1, "POST", "/things", "k", `{"a":1}`)
	w := f.do(u2, "POST", "/things", "k", `{"a":2}`)
	if w.Code != 201 || w.Header().Get(IdempotencyReplayedHeader) != "" || f.calls.Load() != 2 {
		t.Fatalf("other user: %d %v calls=%d", w.Code, w.Header(), f.calls.Load())
	}
}

func TestIdempotencyPassThrough(t *testing.T) {
	f := newIdemFixture(t, http.StatusCreated)
	u := f.ids.New()
	f.do(u, "POST", "/things", "", `{}`)
	f.do(u, "POST", "/things", "", `{}`)
	f.do(u, "PUT", "/things", "k", `{}`)
	f.do(u, "PUT", "/things", "k", `{}`)
	f.do(domain.ID{}, "POST", "/things", "k", `{}`)
	f.do(domain.ID{}, "POST", "/things", "k", `{}`)
	if f.calls.Load() != 6 {
		t.Fatalf("calls = %d, want 6 (no caching)", f.calls.Load())
	}
}

func TestIdempotencyDoesNotStoreServerErrors(t *testing.T) {
	f := newIdemFixture(t, http.StatusInternalServerError)
	u := f.ids.New()
	f.do(u, "POST", "/things", "k", `{}`)
	w := f.do(u, "POST", "/things", "k", `{}`)
	if f.calls.Load() != 2 || w.Header().Get(IdempotencyReplayedHeader) != "" {
		t.Fatalf("5xx was stored: calls=%d", f.calls.Load())
	}
}

func TestIdempotencyStoresClientErrors(t *testing.T) {
	f := newIdemFixture(t, http.StatusUnprocessableEntity)
	u := f.ids.New()
	f.do(u, "POST", "/things", "k", `{}`)
	w := f.do(u, "POST", "/things", "k", `{}`)
	if f.calls.Load() != 1 || w.Code != 422 || w.Header().Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("4xx not replayed: calls=%d code=%d", f.calls.Load(), w.Code)
	}
}

func TestIdempotencyKeyTooLong(t *testing.T) {
	f := newIdemFixture(t, http.StatusCreated)
	w := f.do(f.ids.New(), "POST", "/things", strings.Repeat("k", 256), `{}`)
	if w.Code != 400 || f.calls.Load() != 0 {
		t.Fatalf("code %d calls %d", w.Code, f.calls.Load())
	}
}

func TestIdempotencyConcurrentSameKeyRunsOnce(t *testing.T) {
	f := newIdemFixture(t, http.StatusCreated)
	u := f.ids.New()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.do(u, "POST", "/things", "k", `{"a":1}`)
		}()
	}
	wg.Wait()
	if f.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", f.calls.Load())
	}
}
