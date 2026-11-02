// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/web"
)

func init() {
	addFeature(feature{
		Name:  "attempts",
		After: []string{"cards", "problems", "sessions"},
		Build: func(w *wiring) {
			aopts := []attempts.Option{attempts.WithReader(w.st.Attempts()), attempts.WithAttemptRecorder(w.ho.metrics), attempts.WithSessions(w.sessions)}
			if w.ho.tags != nil {
				aopts = append(aopts, attempts.WithTagResolver(w.ho.tags))
			}
			w.attempts = attempts.NewService(store.AttemptsCardTx(w.st, w.mastery.CardUpdater), w.problems, w.clock,
				domain.NewIDGenerator(w.clock), aopts...)
		},
		Routes: func(w *wiring, rt *platform.Router) { attempts.NewHandler(w.attempts, w.logger).Register(rt) },
		Site: func(w *wiring, site *web.Web) {
			site.UseReview(w.sessions, w.attempts, w.problems)
		},
	})
}
