// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/chan-labs/quizzotic/internal/export"
	"github.com/chan-labs/quizzotic/internal/platform"
)

func init() {
	addFeature(feature{
		Name:  "export",
		After: []string{"cards"},
		Build: func(w *wiring) {
			if w.st != nil {
				w.export = w.mastery.ExportService(w.st, w.logger)
			}
		},
		Routes: func(w *wiring, rt *platform.Router) { export.NewHandler(w.export, w.logger).Register(rt) },
	})
}
