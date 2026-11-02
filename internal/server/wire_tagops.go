// SPDX-License-Identifier: Apache-2.0

package server

import "github.com/chan-labs/quizzotic/internal/web"

func init() {
	addFeature(feature{
		Name: "tagops",
		Site: func(w *wiring, site *web.Web) {
			if w.ho.tags != nil {
				site.UseTagOps(w.ho.tags)
			}
		},
	})
}
