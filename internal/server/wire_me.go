// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"

	"github.com/chan-labs/quizzotic/internal/me"
	"github.com/chan-labs/quizzotic/internal/platform"
)

func init() {
	addFeature(feature{
		Name:   "me",
		Routes: func(w *wiring, rt *platform.Router) { me.NewHandler(w.ho.me, w.logger).Register(rt) },
		Root:   func(w *wiring, mux *http.ServeMux) { me.NewHandler(w.ho.me, w.logger).RegisterRoot(mux) },
	})
}
