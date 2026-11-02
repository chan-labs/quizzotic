// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/platform"
)

func init() {
	addFeature(feature{
		Name:  "metrics",
		After: []string{"cards"},
		Build: func(w *wiring) {
			w.queries = w.mastery.Queries(w.st, w.ho.tagsResolver())
		},
		Routes: func(w *wiring, rt *platform.Router) { metrics.NewHandler(w.queries, w.logger).Register(rt) },
	})
}
