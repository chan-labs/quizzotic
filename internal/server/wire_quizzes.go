// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/web"
)

func init() {
	addFeature(feature{
		Name:  "quizzes",
		After: []string{"problems"},
		Build: func(w *wiring) {
			w.quizzes = quizzes.NewService(store.QuizzesTx(w.st), w.problems, w.clock, domain.NewIDGenerator(w.clock))
		},
		Routes: func(w *wiring, rt *platform.Router) { quizzes.NewHandler(w.quizzes, w.logger).Register(rt) },
		Site: func(w *wiring, site *web.Web) {
			site.UseQuizzes(w.quizzes, w.problems, w.sessions)
		},
	})
}
