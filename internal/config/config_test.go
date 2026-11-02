// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = `
server:
  listen: ":9090"
  public_url: "https://quizzotic.example.com"
  session_ttl: "48h"
storage:
  url: "sqlite://data/quizzotic.db"
  seed: ["builtin:fsrs-basics"]
  auto_migrate: false
auth:
  issuers:
    - name: "pocketid"
      issuer: "https://auth.example.com"
      audience: "quizzotic"
      scopes: ["openid", "profile", "email"]
      clients:
        - id: "quizzotic-web"
          kind: "human"
          secret: ""
        - id: "quizzotic-agent"
          kind: "agent"
          allowed_scopes: ["problems:read"]
scheduling:
  desired_retention: 0.85
  learning_steps: ["2m", "15m"]
  new_per_day: 10
logging:
  level: "debug"
  format: "text"
`

// minimalYAML has only the keys with no default.
const minimalYAML = `
server:
  public_url: "https://quizzotic.example.com"
auth:
  issuers:
    - name: "idp"
      issuer: "https://auth.example.com"
      audience: "quizzotic"
      scopes: ["openid"]
`

func TestParse_File(t *testing.T) {
	cfg, err := Parse([]byte(validYAML), []string{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":9090" || cfg.Server.SessionTTL.Std() != 48*time.Hour {
		t.Errorf("server = %+v", cfg.Server)
	}
	if cfg.Storage.AutoMigrate {
		t.Error("auto_migrate should be false")
	}
	if got := cfg.Auth.Issuers[0].Clients[1].AllowedScopes; len(got) != 1 || got[0] != "problems:read" {
		t.Errorf("allowed_scopes = %v", got)
	}
	if got := cfg.Scheduling.LearningSteps; len(got) != 2 || got[1].Std() != 15*time.Minute {
		t.Errorf("learning_steps = %v", got)
	}
	// untouched keys keep their defaults
	if cfg.Scheduling.MaxReviewsPerDay != 200 || cfg.Scheduling.DefaultTimezone != "America/New_York" {
		t.Errorf("scheduling defaults lost: %+v", cfg.Scheduling)
	}
}

func TestParse_Defaults(t *testing.T) {
	cfg, err := Parse([]byte(minimalYAML), []string{})
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	if cfg.Server.Listen != want.Server.Listen || cfg.Server.SessionTTL != want.Server.SessionTTL {
		t.Errorf("server defaults: %+v", cfg.Server)
	}
	if cfg.Storage.URL != "sqlite://quizzotic.db" || !cfg.Storage.AutoMigrate ||
		len(cfg.Storage.Seed) != 1 || cfg.Storage.Seed[0] != "builtin:fsrs-basics" {
		t.Errorf("storage defaults: %+v", cfg.Storage)
	}
	s := cfg.Scheduling
	if s.DesiredRetention != 0.90 || s.NewPerDay != 20 || s.MaxReviewsPerDay != 200 || s.NewInterval != 4 ||
		s.WeakTagBoost != 1.0 || s.RolloverHour != 4 || len(s.LearningSteps) != 2 ||
		s.LearningSteps[0].Std() != time.Minute || s.LearningSteps[1].Std() != 10*time.Minute {
		t.Errorf("scheduling defaults: %+v", s)
	}
	if cfg.Logging.Level != "info" || cfg.Logging.Format != "json" {
		t.Errorf("logging defaults: %+v", cfg.Logging)
	}
}

func TestParse_EmptyFileReportsRequiredKeys(t *testing.T) {
	_, err := Parse(nil, []string{})
	assertProblems(t, err, "auth.issuers: exactly one")
}

func TestValidateForServe(t *testing.T) {
	// Load alone accepts a config that lacks serve-only keys.
	cfg, err := Parse([]byte("auth:\n  issuers:\n    - {}\n"), []string{})
	if err != nil {
		t.Fatal(err)
	}
	assertProblems(t, cfg.ValidateForServe(),
		"server.public_url: required", "auth.issuers[0].name: required", "auth.issuers[0].issuer: required",
		"auth.issuers[0].audience: required", "auth.issuers[0].scopes")

	y := minimalYAML + "      clients:\n        - {secret: x}\n"
	cfg, err = Parse([]byte(y), []string{})
	if err != nil {
		t.Fatal(err)
	}
	assertProblems(t, cfg.ValidateForServe(), "clients[0].id: required", "clients[0].kind: required")

	cfg, err = Parse([]byte(minimalYAML), []string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateForServe(); err != nil {
		t.Errorf("minimal config should be serve-ready: %v", err)
	}
}

func TestParse_UnmatchedEnvIgnored(t *testing.T) {
	env := []string{"QUIZZOTIC_TOKEN=abc", "QUIZZOTIC_CONFIG=/x.yaml", "QUIZZOTIC_SERVER_LISTN=:1", "QUIZZOTIC_NOPE=1"}
	cfg, err := Parse([]byte(minimalYAML), env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":8080" {
		t.Errorf("listen = %q", cfg.Server.Listen)
	}
}

func TestParse_EnvOverrides(t *testing.T) {
	tests := []struct {
		name  string
		env   []string
		check func(t *testing.T, c *Config)
	}{
		{"string", []string{"QUIZZOTIC_SERVER_LISTEN=:1234"}, func(t *testing.T, c *Config) {
			if c.Server.Listen != ":1234" {
				t.Errorf("listen = %q", c.Server.Listen)
			}
		}},
		{"duration", []string{"QUIZZOTIC_SERVER_SESSION_TTL=1h"}, func(t *testing.T, c *Config) {
			if c.Server.SessionTTL.Std() != time.Hour {
				t.Errorf("ttl = %v", c.Server.SessionTTL)
			}
		}},
		{"bool", []string{"QUIZZOTIC_STORAGE_AUTO_MIGRATE=false"}, func(t *testing.T, c *Config) {
			if c.Storage.AutoMigrate {
				t.Error("auto_migrate should be false")
			}
		}},
		{"int", []string{"QUIZZOTIC_SCHEDULING_NEW_PER_DAY=5"}, func(t *testing.T, c *Config) {
			if c.Scheduling.NewPerDay != 5 {
				t.Errorf("new_per_day = %d", c.Scheduling.NewPerDay)
			}
		}},
		{"float", []string{"QUIZZOTIC_SCHEDULING_DESIRED_RETENTION=0.8"}, func(t *testing.T, c *Config) {
			if c.Scheduling.DesiredRetention != 0.8 {
				t.Errorf("retention = %v", c.Scheduling.DesiredRetention)
			}
		}},
		{"string list", []string{"QUIZZOTIC_STORAGE_SEED=builtin:a, builtin:b"}, func(t *testing.T, c *Config) {
			if got := c.Storage.Seed; len(got) != 2 || got[0] != "builtin:a" || got[1] != "builtin:b" {
				t.Errorf("seed = %v", got)
			}
		}},
		{"duration list", []string{"QUIZZOTIC_SCHEDULING_LEARNING_STEPS=30s,5m,1h"}, func(t *testing.T, c *Config) {
			if got := c.Scheduling.LearningSteps; len(got) != 3 || got[0].Std() != 30*time.Second {
				t.Errorf("steps = %v", got)
			}
		}},
		{"empty list", []string{"QUIZZOTIC_STORAGE_SEED="}, func(t *testing.T, c *Config) {
			if len(c.Storage.Seed) != 0 {
				t.Errorf("seed = %v", c.Storage.Seed)
			}
		}},
		{"indexed struct field", []string{"QUIZZOTIC_AUTH_ISSUERS_0_AUDIENCE=other"}, func(t *testing.T, c *Config) {
			if c.Auth.Issuers[0].Audience != "other" {
				t.Errorf("audience = %q", c.Auth.Issuers[0].Audience)
			}
		}},
		{"nested index secret", []string{
			"QUIZZOTIC_AUTH_ISSUERS_0_CLIENTS_0_ID=web",
			"QUIZZOTIC_AUTH_ISSUERS_0_CLIENTS_0_KIND=human",
			"QUIZZOTIC_AUTH_ISSUERS_0_CLIENTS_0_SECRET=s3cret",
		}, func(t *testing.T, c *Config) {
			cl := c.Auth.Issuers[0].Clients
			if len(cl) != 1 || cl[0].Secret != "s3cret" {
				t.Errorf("clients = %+v", cl)
			}
		}},
		{"env overrides file value", []string{"QUIZZOTIC_SERVER_PUBLIC_URL=https://other.example.com"}, func(t *testing.T, c *Config) {
			if c.Server.PublicURL != "https://other.example.com" {
				t.Errorf("public_url = %q", c.Server.PublicURL)
			}
		}},
		{"non-prefixed and QUIZZOTIC_CONFIG ignored", []string{"HOME=/x", "QUIZZOTIC_CONFIG=/etc/q.yaml"}, func(t *testing.T, c *Config) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(minimalYAML), tt.env)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, cfg)
		})
	}
}

func TestParse_EnvCanBuildListFromNothing(t *testing.T) {
	env := []string{
		"QUIZZOTIC_SERVER_PUBLIC_URL=https://quizzotic.example.com",
		"QUIZZOTIC_AUTH_ISSUERS_0_NAME=idp",
		"QUIZZOTIC_AUTH_ISSUERS_0_ISSUER=https://auth.example.com",
		"QUIZZOTIC_AUTH_ISSUERS_0_AUDIENCE=quizzotic",
		"QUIZZOTIC_AUTH_ISSUERS_0_SCOPES=openid,email",
	}
	cfg, err := Parse(nil, env)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Auth.Issuers[0].Scopes; len(got) != 2 || got[1] != "email" {
		t.Errorf("scopes = %v", got)
	}
}

func TestParse_EnvErrors(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{"bad int", []string{"QUIZZOTIC_SCHEDULING_NEW_PER_DAY=many"}, []string{"QUIZZOTIC_SCHEDULING_NEW_PER_DAY: invalid integer"}},
		{"bad bool", []string{"QUIZZOTIC_STORAGE_AUTO_MIGRATE=maybe"}, []string{"QUIZZOTIC_STORAGE_AUTO_MIGRATE: invalid boolean"}},
		{"bad duration", []string{"QUIZZOTIC_SERVER_SESSION_TTL=soon"}, []string{"QUIZZOTIC_SERVER_SESSION_TTL: invalid duration"}},
		{"bad float", []string{"QUIZZOTIC_SCHEDULING_DESIRED_RETENTION=high"}, []string{"invalid number"}},
		{"index too large", []string{"QUIZZOTIC_AUTH_ISSUERS_999_NAME=x"}, []string{"list index 999 is too large"}},
		{"override fails validation", []string{"QUIZZOTIC_LOGGING_LEVEL=loud"}, []string{"logging.level"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(minimalYAML), tt.env)
			assertProblems(t, err, tt.want...)
		})
	}
}

func TestParse_UnknownKeys(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"top level", "bogus: 1\n", "field bogus not found"},
		{"nested", "server:\n  listn: \":1\"\n", "field listn not found"},
		{"in list item", "auth:\n  issuers:\n    - nme: x\n", "field nme not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml), []string{})
			assertProblems(t, err, tt.want)
		})
	}
}

func TestParse_InvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		patch string // appended after minimalYAML-compatible base
		want  string
	}{
		{"wrong type", "scheduling:\n  new_per_day: lots\n", "cannot unmarshal"},
		{"bad duration", "server:\n  session_ttl: forever\n", "invalid duration"},
		{"zero ttl", "server:\n  session_ttl: 0s\n", "server.session_ttl: must be positive"},
		{"retention too high", "scheduling:\n  desired_retention: 1.0\n", "scheduling.desired_retention"},
		{"retention zero", "scheduling:\n  desired_retention: 0\n", "scheduling.desired_retention"},
		{"bad step", "scheduling:\n  learning_steps: [\"-1m\"]\n", "scheduling.learning_steps[0]"},
		{"negative new", "scheduling:\n  new_per_day: -1\n", "scheduling.new_per_day"},
		{"new interval", "scheduling:\n  new_interval: 0\n", "scheduling.new_interval"},
		{"rollover", "scheduling:\n  rollover_hour: 24\n", "scheduling.rollover_hour"},
		{"timezone", "scheduling:\n  default_timezone: Mars/Base\n", "scheduling.default_timezone"},
		{"log level", "logging:\n  level: loud\n", "logging.level"},
		{"log format", "logging:\n  format: xml\n", "logging.format"},
		{"empty listen", "server:\n  listen: \"\"\n", "server.listen"},
		{"relative public url", "server:\n  public_url: quizzotic.example.com\n", "server.public_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The patch comes first; duplicate top-level keys are avoided by
			// using a base that only contains what the patch does not.
			base := "auth:\n  issuers:\n    - {name: idp, issuer: \"https://auth.example.com\", audience: q, scopes: [openid]}\n"
			if !strings.Contains(tt.patch, "server:") {
				base += "server:\n  public_url: \"https://quizzotic.example.com\"\n"
			}
			_, err := Parse([]byte(base+tt.patch), []string{})
			assertProblems(t, err, tt.want)
		})
	}
}

func TestParse_IssuerAndClientValidation(t *testing.T) {
	base := "server:\n  public_url: \"https://quizzotic.example.com\"\n"
	tests := []struct {
		name string
		auth string
		want []string
	}{
		{"no issuers", "auth:\n  issuers: []\n", []string{"auth.issuers: exactly one"}},
		{"two issuers", "auth:\n  issuers:\n    - {name: a, issuer: \"https://a.example.com\", audience: q, scopes: [openid]}\n    - {name: b, issuer: \"https://b.example.com\", audience: q, scopes: [openid]}\n",
			[]string{"auth.issuers: exactly one"}},
		{"bad issuer url", "auth:\n  issuers:\n    - {name: a, issuer: \"auth.example.com\", audience: q, scopes: [openid]}\n",
			[]string{"auth.issuers[0].issuer"}},
		{"client problems", "auth:\n  issuers:\n    - name: a\n      issuer: \"https://a.example.com\"\n      audience: q\n      scopes: [openid]\n      clients:\n        - {id: x, kind: human, acts_for: someone}\n        - {id: x, kind: robot}\n        - {kind: agent}\n",
			[]string{"clients[0].acts_for", "clients[1].id: duplicate", "clients[1].kind"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(base+tt.auth), []string{})
			assertProblems(t, err, tt.want...)
		})
	}
}

func TestParse_ReportsEveryErrorAtOnce(t *testing.T) {
	y := `
bogus: true
server:
  listn: ":1"
  session_ttl: nonsense
storage:
  url: "mysql://db"
scheduling:
  rollover_hour: 99
logging:
  level: loud
`
	_, err := Parse([]byte(y), []string{"QUIZZOTIC_SCHEDULING_NEW_PER_DAY=x"})
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("want *Error, got %v", err)
	}
	assertProblems(t, err,
		"field bogus not found", "field listn not found", "invalid duration",
		"unknown scheme", "invalid integer",
		"scheduling.rollover_hour", "logging.level", "auth.issuers")
	if len(ce.Problems) < 8 {
		t.Errorf("expected at least 8 problems, got %d: %v", len(ce.Problems), ce.Problems)
	}
}

func TestParseStorageURL(t *testing.T) {
	tests := []struct {
		raw     string
		driver  Driver
		target  string
		wantErr string
	}{
		{"sqlite://quizzotic.db", DriverSQLite, "quizzotic.db", ""},
		{"sqlite://path/to/file.db", DriverSQLite, "path/to/file.db", ""},
		{"sqlite:///var/lib/q/q.db", DriverSQLite, "/var/lib/q/q.db", ""},
		{"sqlite://:memory:", DriverSQLite, ":memory:", ""},
		{"SQLITE://x.db", DriverSQLite, "x.db", ""},
		{"postgres://user:pass@db.example.com/quizzotic", DriverPostgres, "postgres://user:pass@db.example.com/quizzotic", ""},
		{"postgres://db.example.com:5432/q?sslmode=disable", DriverPostgres, "postgres://db.example.com:5432/q?sslmode=disable", ""},
		{"postgresql://user:pass@db.example.com/quizzotic", DriverPostgres, "postgresql://user:pass@db.example.com/quizzotic", ""},
		{"postgresql://db.example.com:5432/q?sslmode=disable", DriverPostgres, "postgresql://db.example.com:5432/q?sslmode=disable", ""},
		{"POSTGRESQL://db.example.com/q", DriverPostgres, "postgresql://db.example.com/q", ""},
		{"sqlite://", "", "", "needs a file path"},
		{"postgresql:///nohost", "", "", "postgresql:// needs a host"},
		{"postgres:///nohost", "", "", "needs a host"},
		{"mysql://u@h/db", "", "", `unknown scheme "mysql"`},
		{"http://example.com", "", "", "unknown scheme"},
		{"quizzotic.db", "", "", "missing scheme"},
		{"", "", "", "missing scheme"},
		{"://x", "", "", "missing scheme"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			d, target, err := ParseStorageURL(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d != tt.driver || target != tt.target {
				t.Errorf("got (%q, %q), want (%q, %q)", d, target, tt.driver, tt.target)
			}
		})
	}
}

func TestParseStorageURL_ErrorsDoNotLeakSecrets(t *testing.T) {
	for _, raw := range []string{"user:hunter2@db.example.com/q", "hunter2", "mysql://user:hunter2@db.example.com/q", "postgres://user:hunter2@/q", "postgresql://user:hunter2@/q"} {
		_, _, err := ParseStorageURL(raw)
		if err == nil {
			t.Fatalf("%q: expected error", raw)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%q: error leaks secret: %v", raw, err)
		}
	}
}

func TestStorageDriver(t *testing.T) {
	for _, u := range []string{"postgres://db.example.com/q", "postgresql://db.example.com/q"} {
		cfg, err := Parse([]byte(minimalYAML), []string{"QUIZZOTIC_STORAGE_URL=" + u})
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		if d, target := cfg.StorageDriver(); d != DriverPostgres || target != u {
			t.Errorf("%s: driver = %q target = %q", u, d, target)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quizzotic.yaml")
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, []string{"QUIZZOTIC_LOGGING_LEVEL=warn"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Logging.Level != "warn" {
		t.Errorf("level = %q", cfg.Logging.Level)
	}

	_, err = Load(filepath.Join(dir, "missing.yaml"), []string{})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file err = %v", err)
	}
}

func TestObservability_MetricsOffByDefault(t *testing.T) {
	cfg, err := Parse([]byte(minimalYAML), []string{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Observability.MetricsEnabled {
		t.Error("metrics must be off by default")
	}
}

func TestObservability_MetricsEnabledFromYAMLAndEnv(t *testing.T) {
	cfg, err := Parse([]byte(minimalYAML+"observability:\n  metrics_enabled: true\n"), []string{})
	if err != nil || !cfg.Observability.MetricsEnabled {
		t.Fatalf("yaml: %v", err)
	}
	cfg, err = Parse([]byte(minimalYAML), []string{"QUIZZOTIC_OBSERVABILITY_METRICS_ENABLED=true"})
	if err != nil || !cfg.Observability.MetricsEnabled {
		t.Fatalf("env: %v", err)
	}
}

func TestLoad_DefaultPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "quizzotic.yaml"), []byte(minimalYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if _, err := Load("", []string{}); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_UsesProcessEnvWhenNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "q.yaml")
	if err := os.WriteFile(path, []byte(minimalYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUIZZOTIC_SERVER_LISTEN", ":4321")
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":4321" {
		t.Errorf("listen = %q", cfg.Server.Listen)
	}
}

// assertProblems requires err to be a config error whose problems collectively
// contain every wanted substring.
func assertProblems(t *testing.T, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error missing %q:\n%v", w, err)
		}
	}
}
