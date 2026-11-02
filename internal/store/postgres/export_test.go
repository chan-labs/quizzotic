// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/chan-labs/quizzotic/internal/export"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
	"github.com/chan-labs/quizzotic/internal/store/storetest"
)

// TestExportSQLiteToPostgresAndBack is the M9.2 check: a rich data set built
// in SQLite exports, imports into an empty Postgres, and exports again to
// byte-identical JSON; the same document then goes back into a fresh SQLite
// and exports identically a third time.
func TestExportSQLiteToPostgresAndBack(t *testing.T) {
	ctx := context.Background()
	src, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	if _, err := src.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fx := storetest.RichFixture(t, src)
	first := storetest.ExportBytes(t, src, fx.Owner)

	// Every exported table has data, including the tag operation kinds and
	// a reverted one, so the comparison below covers them.
	var doc struct {
		Tables map[string][]map[string]any `json:"tables"`
	}
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatal(err)
	}
	for _, tb := range export.Tables {
		if len(doc.Tables[tb.Name]) == 0 {
			t.Errorf("table %s is empty in the fixture", tb.Name)
		}
	}
	kinds, reverted, statuses := map[any]bool{}, 0, map[any]bool{}
	for _, op := range doc.Tables["tag_operations"] {
		kinds[op["kind"]], statuses[op["status"]] = true, true
		if op["reverted_at"] != nil {
			reverted++
		}
	}
	for _, k := range []string{"rename", "merge", "split"} {
		if !kinds[k] {
			t.Errorf("no %s tag operation in the fixture", k)
		}
	}
	for _, s := range []string{"proposed", "applied", "rejected"} {
		if !statuses[s] {
			t.Errorf("no %s tag operation in the fixture", s)
		}
	}
	if reverted == 0 {
		t.Error("no reverted tag operation in the fixture")
	}

	pg := newSchemaStore(t)
	if _, err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := storetest.ImportBytes(t, pg, first); err != nil {
		t.Fatalf("import into postgres: %v", err)
	}
	second := storetest.ExportBytes(t, pg, fx.Owner)
	if !bytes.Equal(first, second) {
		t.Fatalf("export after SQLite -> Postgres differs:\n%s\n----\n%s", first, second)
	}

	back, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "back.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { back.Close() })
	if _, err := back.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := storetest.ImportBytes(t, back, second); err != nil {
		t.Fatalf("import back into sqlite: %v", err)
	}
	if third := storetest.ExportBytes(t, back, fx.Owner); !bytes.Equal(first, third) {
		t.Fatalf("export after Postgres -> SQLite differs:\n%s\n----\n%s", first, third)
	}
}
