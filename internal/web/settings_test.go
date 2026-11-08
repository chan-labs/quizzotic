// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/settings"
	"github.com/chan-labs/quizzotic/internal/web"
)

type memSettings struct{ o settings.Overrides }

func (m *memSettings) Get(context.Context, domain.ID) (settings.Overrides, error) { return m.o, nil }
func (m *memSettings) Replace(_ context.Context, _ domain.ID, o settings.Overrides) error {
	m.o = o
	return nil
}

type memTx struct{ m *memSettings }

func (t memTx) InTx(_ context.Context, fn func(settings.Repos) error) error {
	return fn(settings.Repos{Settings: t.m})
}

func settingsMux(t *testing.T) (*http.ServeMux, *settings.Service, *memSettings) {
	t.Helper()
	mem := &memSettings{}
	svc := settings.NewService(memTx{mem}, settings.DefaultsFromConfig(config.Default().Scheduling))
	w, err := web.New(web.Config{Gate: fakeGate})
	if err != nil {
		t.Fatal(err)
	}
	w.UseSettings(svc)
	mux := http.NewServeMux()
	w.Register(mux)
	return mux, svc, mem
}

func post(mux *http.ServeMux, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "ok", Value: "1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func validForm() url.Values {
	return url.Values{
		"csrf_token":          {testCSRF},
		"desired_retention":   {"0.9"},
		"new_per_day":         {"20"},
		"max_reviews_per_day": {"200"},
		"timezone":            {"America/New_York"},
		"rollover_hour":       {"4"},
	}
}

func TestSettingsPageRendersFormWithCSRF(t *testing.T) {
	mux, _, _ := settingsMux(t)
	rec := get(mux, "/settings", true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `name="new_per_day"`) || !strings.Contains(body, `name="csrf_token" value="`+testCSRF+`"`) {
		t.Fatalf("status %d body %s", rec.Code, body)
	}
	if get(mux, "/settings", false).Code != http.StatusSeeOther {
		t.Fatal("anonymous not redirected")
	}
}

// Changing new_per_day on the page changes the value the next session reads
// from the settings service.
func TestSavingNewPerDayChangesEffectiveSetting(t *testing.T) {
	mux, svc, _ := settingsMux(t)
	var id domain.ID
	before, _ := svc.Get(context.Background(), id)
	f := validForm()
	f.Set("new_per_day", "7")
	rec := post(mux, f)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	after, _ := svc.Get(context.Background(), id)
	if before.NewPerDay == 7 || after.NewPerDay != 7 {
		t.Fatalf("new_per_day before=%d after=%d", before.NewPerDay, after.NewPerDay)
	}
}

func TestSettingsValidationRedisplaysWithErrors(t *testing.T) {
	mux, _, mem := settingsMux(t)
	bad := map[string]string{
		"desired_retention": "1.5", "new_per_day": "-1", "rollover_hour": "24",
		"timezone": "Not/AZone", "max_reviews_per_day": "abc",
	}
	for field, v := range bad {
		f := validForm()
		f.Set(field, v)
		rec := post(mux, f)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `class="error"`) {
			t.Errorf("%s: status %d", field, rec.Code)
		}
	}
	if (mem.o != settings.Overrides{}) {
		t.Errorf("invalid submissions stored overrides: %+v", mem.o)
	}
}

func TestSettingsPostRequiresCSRF(t *testing.T) {
	mux, _, mem := settingsMux(t)
	f := validForm()
	f.Del("csrf_token")
	if rec := post(mux, f); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
	if (mem.o != settings.Overrides{}) {
		t.Fatal("stored without CSRF")
	}
}

func TestSettingsSavePreservesUneditedOverrides(t *testing.T) {
	mux, _, mem := settingsMux(t)
	ni := 9
	mem.o.NewInterval = &ni
	if rec := post(mux, validForm()); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if mem.o.NewInterval == nil || *mem.o.NewInterval != 9 {
		t.Fatal("new_interval override lost")
	}
}
