// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func init() {
	addFeature(feature{
		Name:   "tags",
		Routes: func(w *wiring, rt *platform.Router) { tags.NewHandler(w.ho.tags, w.logger).Register(rt) },
	})
}
