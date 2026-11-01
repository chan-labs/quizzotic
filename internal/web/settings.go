// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// SettingsService is the part of the settings service the Settings page uses.
type SettingsService interface {
	Get(ctx context.Context, userID domain.ID) (settings.Settings, error)
	Overrides(ctx context.Context, userID domain.ID) (settings.Overrides, error)
	Update(ctx context.Context, userID domain.ID, o settings.Overrides) (settings.Settings, error)
}

// settingsForm is the Settings page view model. Values are the raw strings
// shown in the inputs so a rejected submission is redisplayed as typed.
type settingsForm struct {
	Retention, NewPerDay, MaxReviews, Timezone, Rollover string
	Errors                                               map[string]string
	Saved                                                bool
}

func formFrom(s settings.Settings) settingsForm {
	return settingsForm{
		Retention:  strconv.FormatFloat(s.DesiredRetention, 'f', -1, 64),
		NewPerDay:  strconv.Itoa(s.NewPerDay),
		MaxReviews: strconv.Itoa(s.MaxReviewsPerDay),
		Timezone:   s.Timezone,
		Rollover:   strconv.Itoa(s.RolloverHour),
	}
}

func (w *Web) settingsPage(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	cur, err := w.sets.Get(r.Context(), ws.UserID)
	if err != nil {
		w.fail(rw, err)
		return
	}
	f := formFrom(cur)
	f.Saved = r.URL.Query().Get("saved") == "1"
	w.render(rw, r, "settings", page{Title: "Settings", Data: f})
}

func (w *Web) settingsSave(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	if !auth.CheckCSRF(r, ws) {
		platform.WriteError(rw, r, platform.Forbidden("missing or invalid CSRF token"))
		return
	}
	f := settingsForm{
		Retention:  strings.TrimSpace(r.PostFormValue("desired_retention")),
		NewPerDay:  strings.TrimSpace(r.PostFormValue("new_per_day")),
		MaxReviews: strings.TrimSpace(r.PostFormValue("max_reviews_per_day")),
		Timezone:   strings.TrimSpace(r.PostFormValue("timezone")),
		Rollover:   strings.TrimSpace(r.PostFormValue("rollover_hour")),
		Errors:     map[string]string{},
	}
	// Start from the stored overrides so fields this page does not edit keep
	// their state. A blank input clears the override (use the default).
	o, err := w.sets.Overrides(r.Context(), ws.UserID)
	if err != nil {
		w.fail(rw, err)
		return
	}
	o.DesiredRetention = parseOpt(f.Retention, "desired_retention", f.Errors, parseFloat)
	o.NewPerDay = parseOpt(f.NewPerDay, "new_per_day", f.Errors, strconv.Atoi)
	o.MaxReviewsPerDay = parseOpt(f.MaxReviews, "max_reviews_per_day", f.Errors, strconv.Atoi)
	o.RolloverHour = parseOpt(f.Rollover, "rollover_hour", f.Errors, strconv.Atoi)
	o.Timezone = nil
	if f.Timezone != "" {
		o.Timezone = &f.Timezone
	}
	if len(f.Errors) == 0 {
		_, err = w.sets.Update(r.Context(), ws.UserID, o)
		var ve *settings.ValidationError
		switch {
		case errors.As(err, &ve):
			for _, fe := range ve.Fields {
				f.Errors[fe.Field] = fe.Detail
			}
		case err != nil:
			w.fail(rw, err)
			return
		default:
			http.Redirect(rw, r, "/settings?saved=1", http.StatusSeeOther)
			return
		}
	}
	w.renderStatus(rw, r, http.StatusUnprocessableEntity, "settings", page{Title: "Settings", Data: f})
}

func parseOpt[T int | float64](s, field string, errs map[string]string, parse func(string) (T, error)) *T {
	if s == "" {
		return nil
	}
	v, err := parse(s)
	if err != nil {
		errs[field] = "must be a number"
		return nil
	}
	return &v
}

func parseFloat(s string) (float64, error) { return strconv.ParseFloat(s, 64) }

// UseSettings enables the Settings page.
func (w *Web) UseSettings(s SettingsService) { w.sets = s }

func (w *Web) registerSettings(mux *http.ServeMux) {
	if w.sets == nil {
		return
	}
	mux.Handle("GET /settings", w.gate(http.HandlerFunc(w.settingsPage)))
	mux.Handle("POST /settings", w.gate(http.HandlerFunc(w.settingsSave)))
}
