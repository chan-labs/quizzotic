// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/web"
)

func init() {
	addFeature(feature{
		Name:  "sessions",
		After: []string{"problems", "cards"},
		Build: func(w *wiring) {
			// w.ho.metrics may be nil: Metrics methods tolerate a nil receiver.
			w.sessions = review.NewSessionService(w.mastery.SessionTx(w.st), w.problems, w.ho.metrics, w.clock, domain.NewIDGenerator(w.clock))
		},
		Routes: func(w *wiring, rt *platform.Router) { review.NewSessionHandler(w.sessions, w.logger).Register(rt) },
		Site: func(w *wiring, site *web.Web) {
			site.UseToday(w.mastery.Builder(w.st), w.sessions, w.clock.Now)
		},
	})
}
