// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// IdempotencyKeyHeader is the request header carrying the client's key.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotencyReplayedHeader is set to "true" on a replayed response.
const IdempotencyReplayedHeader = "Idempotency-Replayed"

// MaxIdempotencyKeyLen matches the OpenAPI schema for the header.
const MaxIdempotencyKeyLen = 255

// TypeIdempotencyKeyReuse identifies a key reused with a different request.
const TypeIdempotencyKeyReuse = "urn:quizzotic:problem:idempotency-key-reuse"

// replayedHeaders are the response headers stored and replayed.
var replayedHeaders = []string{"Content-Type", "Location", "ETag"}

// Idempotency returns middleware that gives POST requests carrying an
// Idempotency-Key header at-most-once semantics per user. The first request
// runs normally and its response (status, body, Content-Type, Location, ETag)
// is stored for IdempotencyTTL. A repeat with the same key and the same
// method, path and body gets the stored response with "Idempotency-Replayed:
// true"; the same key with a different request gets 422. Server errors (5xx)
// are not stored, so the client may retry them. Requests without the header,
// non-POST requests, and requests with no authenticated user pass through.
//
// userID extracts the authenticated owner from the request context. Install
// the middleware after authentication.
func Idempotency(repo IdempotencyRepo, clock domain.Clock, userID func(context.Context) (domain.ID, bool)) func(http.Handler) http.Handler {
	locks := &keyLocks{m: map[string]*keyLock{}}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyKeyHeader)
			if r.Method != http.MethodPost || key == "" {
				next.ServeHTTP(w, r)
				return
			}
			uid, ok := userID(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if len(key) > MaxIdempotencyKeyLen {
				WriteError(w, r, BadRequest("Idempotency-Key is longer than 255 characters"))
				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				var mbe *http.MaxBytesError
				if errors.As(err, &mbe) {
					WriteError(w, r, NewProblem(TypeTooLarge, http.StatusRequestEntityTooLarge, "request body too large"))
				} else {
					WriteError(w, r, BadRequest("could not read request body"))
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			hash := requestHash(r, body)

			// Serialize concurrent requests with the same key so the later one
			// replays instead of running the handler twice.
			unlock := locks.lock(uid.String() + "\x00" + key)
			defer unlock()

			rec, err := repo.Get(r.Context(), uid, key, clock.Now())
			switch {
			case err == nil:
				if rec.RequestHash != hash {
					WriteError(w, r, NewProblem(TypeIdempotencyKeyReuse, http.StatusUnprocessableEntity,
						"Idempotency-Key was already used with a different request"))
					return
				}
				replay(w, rec)
				return
			case !errors.Is(err, ErrIdempotencyNotFound):
				slog.ErrorContext(r.Context(), "idempotency lookup failed", "err", err)
				WriteError(w, r, err)
				return
			}

			cw := &captureWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(cw, r)
			if cw.status >= 500 || cw.status < 200 {
				return
			}
			now := clock.Now()
			stored := IdempotencyRecord{
				UserID: uid, Key: key, RequestHash: hash, Status: cw.status,
				Header: http.Header{}, Body: cw.buf.Bytes(),
				CreatedAt: now, ExpiresAt: now.Add(IdempotencyTTL),
			}
			for _, h := range replayedHeaders {
				if v := cw.Header().Values(h); len(v) > 0 {
					stored.Header[h] = v
				}
			}
			// The response is already sent; a failed store only costs the
			// client its replay protection.
			if _, err := repo.Put(r.Context(), stored); err != nil {
				slog.ErrorContext(r.Context(), "idempotency store failed", "err", err)
			}
		})
	}
}

// SweepIdempotency deletes expired records every interval until ctx is done.
func SweepIdempotency(ctx context.Context, repo IdempotencyRepo, clock domain.Clock, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := repo.PurgeExpired(ctx, clock.Now()); err != nil {
				log.ErrorContext(ctx, "idempotency sweep failed", "err", err)
			} else if n > 0 {
				log.InfoContext(ctx, "idempotency sweep", "removed", n)
			}
		}
	}
}

func requestHash(r *http.Request, body []byte) string {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.RawQuery))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func replay(w http.ResponseWriter, rec IdempotencyRecord) {
	for k, v := range rec.Header {
		w.Header()[k] = v
	}
	w.Header().Set(IdempotencyReplayedHeader, "true")
	w.WriteHeader(rec.Status)
	_, _ = w.Write(rec.Body)
}

// captureWriter tees the response body and status while passing them through.
type captureWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	buf         bytes.Buffer
}

func (c *captureWriter) WriteHeader(code int) {
	if !c.wroteHeader {
		c.status, c.wroteHeader = code, true
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	c.wroteHeader = true
	c.buf.Write(b)
	return c.ResponseWriter.Write(b)
}

func (c *captureWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// keyLocks is a set of mutexes by key, freed when unused.
type keyLocks struct {
	mu sync.Mutex
	m  map[string]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyLocks) lock(key string) func() {
	k.mu.Lock()
	l := k.m[key]
	if l == nil {
		l = &keyLock{}
		k.m[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}
