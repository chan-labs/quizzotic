// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"net/http"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/review"
)

// TodayReader is what the Today page needs to read from the review feature.
// *review.Builder implements it.
type TodayReader interface {
	Today(ctx context.Context, userID domain.ID, now time.Time) (review.TodayView, error)
}

// SessionStarter creates review sessions, through the service the API uses.
// *review.SessionService implements it.
type SessionStarter interface {
	Create(ctx context.Context, userID domain.ID, in review.CreateSessionInput) (review.SessionView, error)
}

type todayPages struct {
	reader  TodayReader
	starter SessionStarter
	now     func() time.Time
}

// UseToday enables the Today page. Call it before Register. now supplies the
// current time (nil means time.Now).
func (w *Web) UseToday(r TodayReader, s SessionStarter, now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	w.todayPg = todayPages{reader: r, starter: s, now: now}
}

// reviewPath is where Start review lands: the Review page.
func reviewPath(id domain.ID) string { return "/review/" + id.String() }

func (w *Web) registerToday(mux *http.ServeMux) {
	if w.todayPg.starter == nil {
		return
	}
	mux.Handle("POST /review/start", w.gate(http.HandlerFunc(w.startReview)))
}

func (w *Web) todayPage(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	v, err := w.todayPg.reader.Today(r.Context(), ws.UserID, w.todayPg.now())
	if err != nil {
		w.fail(rw, err)
		return
	}
	w.render(rw, r, "today", page{Title: "Today", Data: v})
}

// startReview creates a review session and sends the browser to it.
func (w *Web) startReview(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	if !auth.CheckCSRF(r, ws) {
		http.Error(rw, "forbidden", http.StatusForbidden)
		return
	}
	s, err := w.todayPg.starter.Create(r.Context(), ws.UserID, review.CreateSessionInput{Kind: review.SessionReview})
	if err != nil {
		w.fail(rw, err)
		return
	}
	http.Redirect(rw, r, reviewPath(s.ID), http.StatusSeeOther)
}
