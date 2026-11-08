// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
)

// Overrides are a user's stored scheduling settings. A nil field means "use
// the configured default".
type Overrides struct {
	DesiredRetention *float64
	NewPerDay        *int
	MaxReviewsPerDay *int
	NewInterval      *int
	WeakTagBoost     *float64
	RolloverHour     *int
	Timezone         *string // IANA name
}

// Settings are a user's effective scheduling settings: overrides resolved
// against the configured defaults.
type Settings struct {
	DesiredRetention float64
	NewPerDay        int
	MaxReviewsPerDay int
	NewInterval      int
	WeakTagBoost     float64
	RolloverHour     int
	Timezone         string
}

// Defaults are the server-wide scheduling defaults from configuration.
type Defaults = Settings

// DefaultsFromConfig extracts the settings defaults from the scheduling
// section of the configuration.
func DefaultsFromConfig(c config.Scheduling) Defaults {
	return Defaults{
		DesiredRetention: c.DesiredRetention,
		NewPerDay:        c.NewPerDay,
		MaxReviewsPerDay: c.MaxReviewsPerDay,
		NewInterval:      c.NewInterval,
		WeakTagBoost:     c.WeakTagBoost,
		RolloverHour:     c.RolloverHour,
		Timezone:         c.DefaultTimezone,
	}
}

// Resolve applies o on top of d.
func Resolve(d Defaults, o Overrides) Settings {
	s := d
	if o.DesiredRetention != nil {
		s.DesiredRetention = *o.DesiredRetention
	}
	if o.NewPerDay != nil {
		s.NewPerDay = *o.NewPerDay
	}
	if o.MaxReviewsPerDay != nil {
		s.MaxReviewsPerDay = *o.MaxReviewsPerDay
	}
	if o.NewInterval != nil {
		s.NewInterval = *o.NewInterval
	}
	if o.WeakTagBoost != nil {
		s.WeakTagBoost = *o.WeakTagBoost
	}
	if o.RolloverHour != nil {
		s.RolloverHour = *o.RolloverHour
	}
	if o.Timezone != nil {
		s.Timezone = *o.Timezone
	}
	return s
}

// Location loads the settings' time zone.
func (s Settings) Location() (*time.Location, error) { return loadZone(s.Timezone) }

// StudyDay returns the study day containing now under these settings.
func (s Settings) StudyDay(now time.Time) (StudyDay, error) {
	loc, err := s.Location()
	if err != nil {
		return StudyDay{}, err
	}
	return StudyDayAt(now, loc, s.RolloverHour), nil
}

func loadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("timezone %q is not an IANA zone name", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("timezone %q: %w", name, err)
	}
	return loc, nil
}

// FieldError is one invalid field in an update.
type FieldError struct {
	Field  string
	Detail string
}

// ValidationError lists every invalid field in an update.
type ValidationError struct{ Fields []FieldError }

func (e *ValidationError) Error() string {
	s := "settings: invalid"
	var extra strings.Builder
	for i, f := range e.Fields {
		sep := ": "
		if i > 0 {
			sep = "; "
		}
		extra.WriteString(sep + f.Field + " " + f.Detail)
	}
	s += extra.String()
	return s
}

// ErrInvalid matches any *ValidationError under errors.Is.
var ErrInvalid = errors.New("settings: invalid")

// Is makes errors.Is(err, ErrInvalid) true for a ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

// Validate checks the values that are set in o.
func (o Overrides) Validate() error {
	var fs []FieldError
	add := func(f, d string) { fs = append(fs, FieldError{f, d}) }
	if v := o.DesiredRetention; v != nil && !(*v > 0 && *v < 1) {
		add("desired_retention", "must be greater than 0 and less than 1")
	}
	if v := o.NewPerDay; v != nil && *v < 0 {
		add("new_per_day", "must be 0 or more")
	}
	if v := o.MaxReviewsPerDay; v != nil && *v < 0 {
		add("max_reviews_per_day", "must be 0 or more")
	}
	if v := o.NewInterval; v != nil && *v < 1 {
		add("new_interval", "must be 1 or more")
	}
	if v := o.WeakTagBoost; v != nil && !(*v >= 0) {
		add("weak_tag_boost", "must be 0 or more")
	}
	if v := o.RolloverHour; v != nil && (*v < 0 || *v > 23) {
		add("rollover_hour", "must be between 0 and 23")
	}
	if v := o.Timezone; v != nil {
		if _, err := loadZone(*v); err != nil {
			add("timezone", "must be an IANA time zone name such as America/New_York")
		}
	}
	if len(fs) > 0 {
		return &ValidationError{Fields: fs}
	}
	return nil
}
