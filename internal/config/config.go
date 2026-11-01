// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath is the config file used when --config is not given.
const DefaultPath = "./quizzotic.yaml"

// EnvPrefix prefixes every environment override.
const EnvPrefix = "QUIZZOTIC_"

// envIgnored lists QUIZZOTIC_* variables that are not config keys.
var envIgnored = map[string]bool{"QUIZZOTIC_CONFIG": true, "QUIZZOTIC_TOKEN": true}

// Duration is a time.Duration that reads Go duration strings ("720h", "10m").
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("invalid duration %q", string(b))
	}
	*d = Duration(v)
	return nil
}

// UnmarshalYAML reports a bad value as a *yaml.TypeError, which yaml.v3 collects
// and keeps decoding past, so one bad duration does not hide other errors.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	if err := d.UnmarshalText([]byte(s)); err != nil {
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: %v", n.Line, err)}}
	}
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the whole service configuration.
type Config struct {
	Server        Server        `yaml:"server"`
	Storage       Storage       `yaml:"storage"`
	Auth          Auth          `yaml:"auth"`
	Scheduling    Scheduling    `yaml:"scheduling"`
	Logging       Logging       `yaml:"logging"`
	Observability Observability `yaml:"observability"`
}

// Server configures the HTTP listener.
type Server struct {
	Listen     string   `yaml:"listen"`
	PublicURL  string   `yaml:"public_url"`
	SessionTTL Duration `yaml:"session_ttl"`
}

// Storage configures the backend. The scheme of URL picks the driver.
type Storage struct {
	URL         string   `yaml:"url"`
	Seed        []string `yaml:"seed"`
	AutoMigrate bool     `yaml:"auto_migrate"`
}

// Auth configures identity providers (v1: exactly one issuer).
type Auth struct {
	Issuers []Issuer `yaml:"issuers"`
}

// Issuer is one OIDC issuer and the clients allowed to use it.
type Issuer struct {
	Name     string   `yaml:"name"`
	Issuer   string   `yaml:"issuer"`
	Audience string   `yaml:"audience"`
	Scopes   []string `yaml:"scopes"`
	Clients  []Client `yaml:"clients"`
}

// Client is an OAuth client registered for an issuer.
type Client struct {
	ID            string   `yaml:"id"`
	Kind          string   `yaml:"kind"`
	Secret        string   `yaml:"secret"`
	ActsFor       string   `yaml:"acts_for"`
	AllowedScopes []string `yaml:"allowed_scopes"`
}

// Scheduling holds server-wide scheduling defaults; users may override them.
type Scheduling struct {
	DesiredRetention float64    `yaml:"desired_retention"`
	LearningSteps    []Duration `yaml:"learning_steps"`
	NewPerDay        int        `yaml:"new_per_day"`
	MaxReviewsPerDay int        `yaml:"max_reviews_per_day"`
	NewInterval      int        `yaml:"new_interval"`
	WeakTagBoost     float64    `yaml:"weak_tag_boost"`
	RolloverHour     int        `yaml:"rollover_hour"`
	DefaultTimezone  string     `yaml:"default_timezone"`
}

// Logging configures log/slog output.
type Logging struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Observability configures optional operational endpoints.
type Observability struct {
	// MetricsEnabled serves Prometheus text-format metrics at GET /metrics.
	// Off by default. The endpoint is unauthenticated, like /healthz: expose
	// it only to your scraper (bind or firewall it accordingly).
	MetricsEnabled bool `yaml:"metrics_enabled"`
}

// Default returns the configuration used for keys the file leaves out.
// public_url and auth.issuers have no default and are required.
func Default() Config {
	return Config{
		Server: Server{Listen: ":8080", SessionTTL: Duration(720 * time.Hour)},
		Storage: Storage{
			URL:         "sqlite://quizzotic.db",
			Seed:        []string{"builtin:fsrs-basics"},
			AutoMigrate: true,
		},
		Scheduling: Scheduling{
			DesiredRetention: 0.90,
			LearningSteps:    []Duration{Duration(time.Minute), Duration(10 * time.Minute)},
			NewPerDay:        20,
			MaxReviewsPerDay: 200,
			NewInterval:      4,
			WeakTagBoost:     1.0,
			RolloverHour:     4,
			DefaultTimezone:  "America/New_York",
		},
		Logging: Logging{Level: "info", Format: "json"},
	}
}

// Error carries every problem found while loading, so operators fix them in one pass.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	return fmt.Sprintf("invalid configuration (%d errors):\n  - %s",
		len(e.Problems), strings.Join(e.Problems, "\n  - "))
}

// Load reads the YAML file at path (DefaultPath when empty), applies
// QUIZZOTIC_* overrides from environ (os.Environ format; nil means the
// process environment) and validates. A missing file is an error wrapping
// fs.ErrNotExist.
func Load(path string, environ []string) (*Config, error) {
	if path == "" {
		path = DefaultPath
	}
	if environ == nil {
		environ = os.Environ()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("config file %s: %w", path, err)
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data, environ)
}

// Parse is Load without the file read.
func Parse(data []byte, environ []string) (*Config, error) {
	cfg := Default()
	var problems []string

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) { // EOF: empty file, defaults apply
		var te *yaml.TypeError
		if errors.As(err, &te) {
			problems = append(problems, te.Errors...)
		} else {
			problems = append(problems, err.Error())
		}
	}

	problems = append(problems, applyEnv(&cfg, environ)...)
	problems = append(problems, cfg.Validate()...)
	if len(problems) > 0 {
		return nil, &Error{Problems: problems}
	}
	return &cfg, nil
}
