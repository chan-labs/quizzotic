// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/web"
)

// "cards" builds the card projection and the mastery rows that ride on the
// same parameters, so cards project first and mastery follows in the attempt's
// transaction. Features that write attempts or read mastery come after it.
func init() {
	addFeature(feature{
		Name: "cards",
		Build: func(w *wiring) {
			w.cards = store.DefaultCards()
			if w.ho.cards != nil {
				w.cards = *w.ho.cards
			}
			w.mastery = store.NewMastery(w.cards, w.clock)
			w.admin = w.cards.Service(w.st, review.WithLogger(w.logger))
		},
		Routes: func(w *wiring, rt *platform.Router) {
			var tags review.TagResolver
			if w.ho.tags != nil {
				tags = w.ho.tags
			}
			var cards review.Repo
			if w.st != nil {
				cards = w.st.Cards()
			}
			review.NewCardHandler(review.NewCardService(cards, tags), w.logger).Register(rt)
			review.NewAdminHandler(w.admin, w.logger).Register(rt)
		},
		Site: func(w *wiring, site *web.Web) {
			site.UseSettings(w.cards.SettingsService(w.st, review.WithLogger(w.logger)))
		},
	})
}
