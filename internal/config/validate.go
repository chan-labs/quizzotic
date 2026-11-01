// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Driver names a storage backend.
type Driver string

const (
	DriverSQLite   Driver = "sqlite"
	DriverPostgres Driver = "postgres"
)

// ParseStorageURL picks the backend from the URL scheme. For sqlite the
// returned target is the file path (the text after "sqlite://", so relative
// paths work); for postgres it is the full connection URL.
func ParseStorageURL(raw string) (Driver, string, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return "", "", fmt.Errorf("storage.url %q: missing scheme (use sqlite:// or postgres://)", raw)
	}
	switch strings.ToLower(scheme) {
	case "sqlite":
		if rest == "" {
			return "", "", fmt.Errorf("storage.url: sqlite:// needs a file path")
		}
		return DriverSQLite, rest, nil
	case "postgres":
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", fmt.Errorf("storage.url: invalid postgres URL")
		}
		if u.Host == "" {
			return "", "", fmt.Errorf("storage.url: postgres:// needs a host")
		}
		return DriverPostgres, raw, nil
	default:
		return "", "", fmt.Errorf("storage.url: unknown scheme %q (use sqlite:// or postgres://)", scheme)
	}
}

// StorageDriver returns the backend picked by storage.url and its target.
// It is only meaningful on a validated Config.
func (c *Config) StorageDriver() (Driver, string) {
	d, t, _ := ParseStorageURL(c.Storage.URL)
	return d, t
}

// Validate checks every value and returns all problems found.
func (c *Config) Validate() []string {
	var p []string
	add := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }

	// server
	if strings.TrimSpace(c.Server.Listen) == "" {
		add("server.listen: must not be empty")
	}
	if c.Server.PublicURL == "" {
		add("server.public_url: required")
	} else if !isHTTPURL(c.Server.PublicURL) {
		add("server.public_url: %q must be an absolute http(s) URL", c.Server.PublicURL)
	}
	if c.Server.SessionTTL <= 0 {
		add("server.session_ttl: must be positive")
	}

	// storage
	if _, _, err := ParseStorageURL(c.Storage.URL); err != nil {
		p = append(p, err.Error())
	}

	// auth
	if len(c.Auth.Issuers) != 1 {
		add("auth.issuers: exactly one issuer is required in v1 (found %d)", len(c.Auth.Issuers))
	}
	for i, is := range c.Auth.Issuers {
		pre := fmt.Sprintf("auth.issuers[%d]", i)
		if is.Name == "" {
			add("%s.name: required", pre)
		}
		if is.Issuer == "" {
			add("%s.issuer: required", pre)
		} else if !isHTTPURL(is.Issuer) {
			add("%s.issuer: %q must be an absolute http(s) URL", pre, is.Issuer)
		}
		if is.Audience == "" {
			add("%s.audience: required", pre)
		}
		if len(is.Scopes) == 0 {
			add("%s.scopes: at least one scope is required", pre)
		}
		seen := map[string]bool{}
		for j, cl := range is.Clients {
			cp := fmt.Sprintf("%s.clients[%d]", pre, j)
			switch {
			case cl.ID == "":
				add("%s.id: required", cp)
			case seen[cl.ID]:
				add("%s.id: duplicate client id %q", cp, cl.ID)
			default:
				seen[cl.ID] = true
			}
			if cl.Kind != "human" && cl.Kind != "agent" {
				add("%s.kind: %q must be \"human\" or \"agent\"", cp, cl.Kind)
			}
			if cl.ActsFor != "" && cl.Kind != "agent" {
				add("%s.acts_for: only valid for agent clients", cp)
			}
		}
	}

	// scheduling
	s := c.Scheduling
	if !(s.DesiredRetention > 0 && s.DesiredRetention < 1) {
		add("scheduling.desired_retention: %v must be between 0 and 1 (exclusive)", s.DesiredRetention)
	}
	for i, d := range s.LearningSteps {
		if d <= 0 {
			add("scheduling.learning_steps[%d]: must be positive", i)
		}
	}
	if s.NewPerDay < 0 {
		add("scheduling.new_per_day: must not be negative")
	}
	if s.MaxReviewsPerDay < 0 {
		add("scheduling.max_reviews_per_day: must not be negative")
	}
	if s.NewInterval < 1 {
		add("scheduling.new_interval: must be at least 1 day")
	}
	if s.WeakTagBoost < 0 {
		add("scheduling.weak_tag_boost: must not be negative")
	}
	if s.RolloverHour < 0 || s.RolloverHour > 23 {
		add("scheduling.rollover_hour: %d must be 0-23", s.RolloverHour)
	}
	if _, err := time.LoadLocation(s.DefaultTimezone); err != nil || s.DefaultTimezone == "" {
		add("scheduling.default_timezone: %q is not a known IANA timezone", s.DefaultTimezone)
	}

	// logging
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		add("logging.level: %q must be debug, info, warn or error", c.Logging.Level)
	}
	switch c.Logging.Format {
	case "json", "text":
	default:
		add("logging.format: %q must be json or text", c.Logging.Format)
	}
	return p
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
