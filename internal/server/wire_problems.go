// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/web"
)

func init() {
	addFeature(feature{
		Name:  "problems",
		After: []string{"cards"},
		Build: func(w *wiring) {
			popts := []problems.Option{problems.WithBatch(store.ProblemsBatchTx(w.st))}
			if w.ho.tags != nil {
				popts = append(popts, problems.WithTagResolver(w.ho.tags))
			}
			w.problems = problems.NewService(w.st.Problems(), store.ProblemsEditTx(w.st, w.mastery.MajorEdit), w.clock, domain.NewIDGenerator(w.clock), popts...)
		},
		Routes: func(w *wiring, rt *platform.Router) { problems.NewHandler(w.problems, w.logger).Register(rt) },
		Site: func(w *wiring, site *web.Web) {
			if w.ho.tags != nil {
				site.UseProblems(w.problems, w.ho.tags)
			} else {
				site.UseProblems(w.problems, nil)
			}
			site.UseDrafts(w.problems)
		},
	})
}
