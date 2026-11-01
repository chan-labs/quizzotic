// SPDX-License-Identifier: Apache-2.0

package migrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestUpAppliesOnceInOrder(t *testing.T) {
	fsys := fstest.MapFS{
		"d/0002_b.sql": {Data: []byte(`INSERT INTO a VALUES (2);`)},
		"d/0001_a.sql": {Data: []byte(`CREATE TABLE a (n INTEGER);`)},
		"d/.gitkeep":   {Data: nil},
		"d/0003_c.sql": {Data: []byte(`INSERT INTO a VALUES (3);`)},
	}
	db := openDB(t)
	ctx := context.Background()
	got, err := Up(ctx, db, fsys, "d")
	if err != nil || !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("first: %v %v", got, err)
	}
	got, err = Up(ctx, db, fsys, "d")
	if err != nil || len(got) != 0 {
		t.Fatalf("second should be no-op: %v %v", got, err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM a`).Scan(&n)
	if n != 2 {
		t.Fatalf("rows = %d", n)
	}
}

func TestUpFailedMigrationRollsBackAndIsNotRecorded(t *testing.T) {
	fsys := fstest.MapFS{
		"d/0001_a.sql":   {Data: []byte(`CREATE TABLE a (n INTEGER);`)},
		"d/0002_bad.sql": {Data: []byte(`CREATE TABLE b (n INTEGER); INSERT INTO nope VALUES (1);`)},
	}
	db := openDB(t)
	got, err := Up(context.Background(), db, fsys, "d")
	if err == nil || !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("got %v err %v", got, err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='b'`).Scan(&n)
	if n != 0 {
		t.Fatal("partial migration was not rolled back")
	}
	db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n)
	if n != 1 {
		t.Fatalf("recorded %d", n)
	}
}

func TestLoadRejectsBadNamesAndDuplicates(t *testing.T) {
	if _, err := Load(fstest.MapFS{"d/x.sql": {Data: []byte("")}}, "d"); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := Load(fstest.MapFS{"d/1_a.sql": {}, "d/0001_b.sql": {}}, "d"); err == nil {
		t.Fatal("duplicate version accepted")
	}
}

func TestUpDetectsRenamedMigration(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := Up(ctx, db, fstest.MapFS{"d/0001_a.sql": {Data: []byte(`SELECT 1;`)}}, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := Up(ctx, db, fstest.MapFS{"d/0001_other.sql": {Data: []byte(`SELECT 1;`)}}, "d"); err == nil {
		t.Fatal("renamed migration accepted")
	}
}
