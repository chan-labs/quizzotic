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
// paths work); for postgres (postgres:// or postgresql://) it is the full
// connection URL.
func ParseStorageURL(raw string) (Driver, string, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		// Never echo raw: it may carry a database password.
		return "", "", fmt.Errorf("storage.url: missing scheme (use sqlite://, postgres:// or postgresql://)")
	}
	switch strings.ToLower(scheme) {
	case "sqlite":
		if rest == "" {
			return "", "", fmt.Errorf("storage.url: sqlite:// needs a file path")
		}
		return DriverSQLite, rest, nil
	case "postgres", "postgresql":
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", fmt.Errorf("storage.url: invalid postgres URL")
		}
		if u.Host == "" {
			return "", "", fmt.Errorf("storage.url: %s:// needs a host", strings.ToLower(scheme))
		}
		// pgx matches the scheme case-sensitively; the switch above does not.
		return DriverPostgres, strings.ToLower(scheme) + "://" + rest, nil
	default:
		return "", "", fmt.Errorf("storage.url: unknown scheme %q (use sqlite://, postgres:// or postgresql://)", scheme)
	}
}

// StorageDriver returns the backend picked by storage.url and its target.
// It is only meaningful on a validated Config.
func (c *Config) StorageDriver() (Driver, string) {
	d, t, _ := ParseStorageURL(c.Storage.URL)
	return d, t
}

// ForbiddenAgentScopes are the scopes no agent client may hold (spec: Scopes).
var ForbiddenAgentScopes = map[string]bool{
	"attempts:write": true,
	"sessions:write": true,
	"admin":          true,
}

// Validate checks every value and returns all problems found.
func (c *Config) Validate() []string {
	var p []string
	add := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }

	// server
	if strings.TrimSpace(c.Server.Listen) == "" {
		add("server.listen: must not be empty")
	}
	if c.Server.PublicURL != "" && !isHTTPURL(c.Server.PublicURL) {
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
		if is.Issuer != "" && !isHTTPURL(is.Issuer) {
			add("%s.issuer: %q must be an absolute http(s) URL", pre, is.Issuer)
		}
		seen := map[string]bool{}
		for j, cl := range is.Clients {
			cp := fmt.Sprintf("%s.clients[%d]", pre, j)
			if cl.ID != "" {
				if seen[cl.ID] {
					add("%s.id: duplicate client id %q", cp, cl.ID)
				}
				seen[cl.ID] = true
			}
			if cl.Kind != "" && cl.Kind != "human" && cl.Kind != "agent" {
				add("%s.kind: %q must be \"human\" or \"agent\"", cp, cl.Kind)
			}
			if cl.ActsFor != "" && cl.Kind != "agent" {
				add("%s.acts_for: only valid for agent clients", cp)
			}
			if cl.Kind == "agent" {
				for _, sc := range cl.AllowedScopes {
					if ForbiddenAgentScopes[sc] {
						add("%s.allowed_scopes: %q is never allowed on an agent client", cp, sc)
					}
				}
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

// ValidateForServe checks what only `quizzotic serve` needs: the keys that
// have no default and the identity provider details. Commands such as
// `admin migrate`, `admin seed` and `admin export` use Load alone, so they run
// without auth configured. Call it on a Config that already passed Load.
func (c *Config) ValidateForServe() error {
	var p []string
	add := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }

	if c.Server.PublicURL == "" {
		add("server.public_url: required")
	}
	for i, is := range c.Auth.Issuers {
		pre := fmt.Sprintf("auth.issuers[%d]", i)
		if is.Name == "" {
			add("%s.name: required", pre)
		}
		if is.Issuer == "" {
			add("%s.issuer: required", pre)
		}
		if is.Audience == "" {
			add("%s.audience: required", pre)
		}
		if len(is.Scopes) == 0 {
			add("%s.scopes: at least one scope is required", pre)
		}
		for j, cl := range is.Clients {
			cp := fmt.Sprintf("%s.clients[%d]", pre, j)
			if cl.ID == "" {
				add("%s.id: required", cp)
			}
			if cl.Kind == "" {
				add("%s.kind: required (\"human\" or \"agent\")", cp)
			}
		}
	}
	if len(p) > 0 {
		return &Error{Problems: p}
	}
	return nil
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
