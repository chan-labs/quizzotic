// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestMigrateCreatesSchemaMigrationsAndIsIdempotent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "fresh.db")
	url := "sqlite://" + file

	for i := 0; i < 2; i++ {
		code, out, errs := run(t, "migrate", "--storage-url", url)
		if code != 0 {
			t.Fatalf("run %d: exit %d stderr=%s", i+1, code, errs)
		}
		want := "applied 16 migration(s): [1 2 3 4 5 6 7 8 9 10 11 12 13 14 20261102130000 20261102150000]"
		if i == 1 {
			want = "schema up to date"
		}
		if !strings.Contains(out, want) {
			t.Fatalf("run %d: output %q, want %q", i+1, out, want)
		}
	}

	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='schema_migrations'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema_migrations missing: n=%d err=%v", n, err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
}

func TestMigrateFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "q.yaml")
	writeFile(t, cfg, "server:\n  public_url: https://quizzotic.example.com\nstorage:\n  url: sqlite://"+filepath.Join(dir, "c.db")+"\nauth:\n  issuers:\n    - name: idp\n      issuer: https://auth.example.com\n      audience: quizzotic\n      scopes: [openid]\n")
	if code, _, errs := run(t, "migrate", "--config", cfg); code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
}

func TestMigratePostgresUnreachableDoesNotLeakCredentials(t *testing.T) {
	code, _, errs := run(t, "migrate", "--storage-url", "postgres://u:p@127.0.0.1:1/q?connect_timeout=2")
	if code == 0 || !strings.Contains(errs, "postgres") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if strings.Contains(errs, "u:p") {
		t.Fatal("error leaks credentials")
	}
}

func TestMigrateUnknownScheme(t *testing.T) {
	if code, _, errs := run(t, "migrate", "--storage-url", "mysql://x/y"); code == 0 || !strings.Contains(errs, "unknown scheme") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestUsage(t *testing.T) {
	if code, _, _ := run(t); code != 2 {
		t.Fatalf("code=%d", code)
	}
	if code, _, _ := run(t, "nope"); code != 2 {
		t.Fatalf("code=%d", code)
	}
}
