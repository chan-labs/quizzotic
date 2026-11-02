// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

const postgresEnv = "QUIZZOTIC_TEST_POSTGRES_URL"

func TestOpenSQLite(t *testing.T) {
	s, err := Open(context.Background(), "sqlite://"+t.TempDir()+"/q.db")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

// Both spellings must reach the Postgres backend. With nothing listening the
// connection fails, but with a Postgres error, never a scheme error, and never
// one that echoes the password.
func TestOpenPostgresSchemesRouteToPostgres(t *testing.T) {
	for _, scheme := range []string{"postgres", "postgresql"} {
		t.Run(scheme, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := Open(ctx, scheme+"://user:hunter2@127.0.0.1:1/q?sslmode=disable&connect_timeout=1")
			if err == nil {
				t.Fatal("expected a connection error")
			}
			msg := err.Error()
			if strings.Contains(msg, "scheme") || strings.Contains(msg, "unsupported driver") {
				t.Errorf("scheme rejected: %v", err)
			}
			if !strings.Contains(msg, "postgres") {
				t.Errorf("not routed to the postgres store: %v", err)
			}
			if strings.Contains(msg, "hunter2") {
				t.Errorf("error leaks password: %v", err)
			}
		})
	}
}

// With a real Postgres, both schemes open the store.
func TestOpenPostgresSchemesLive(t *testing.T) {
	raw := os.Getenv(postgresEnv)
	if raw == "" {
		if os.Getenv("QUIZZOTIC_REQUIRE_POSTGRES") != "" {
			t.Fatalf("%s is not set but QUIZZOTIC_REQUIRE_POSTGRES is", postgresEnv)
		}
		t.Skipf("%s not set", postgresEnv)
	}
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		t.Fatalf("bad %s", postgresEnv)
	}
	for _, scheme := range []string{"postgres", "postgresql"} {
		t.Run(scheme, func(t *testing.T) {
			s, err := Open(context.Background(), scheme+"://"+rest)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
		})
	}
}
