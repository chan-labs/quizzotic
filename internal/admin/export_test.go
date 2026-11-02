// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/store/driver"
	"github.com/chan-labs/quizzotic/internal/store/storetest"
)

func TestExportAndImportCommands(t *testing.T) {
	dir := t.TempDir()
	srcURL := "sqlite://" + filepath.Join(dir, "src.db")
	if code, _, errs := run(t, "migrate", "--storage-url", srcURL); code != 0 {
		t.Fatalf("migrate: %s", errs)
	}
	st, err := driver.Open(context.Background(), srcURL)
	if err != nil {
		t.Fatal(err)
	}
	fx := storetest.RichFixture(t, st)
	st.Close()

	// Two users: the owner must be named.
	if code, _, errs := run(t, "export", "--storage-url", srcURL); code != 1 || !strings.Contains(errs, "--user") {
		t.Errorf("export without --user: code=%d stderr=%q", code, errs)
	}
	if code, _, errs := run(t, "export", "--storage-url", srcURL, "--user", "nope"); code != 2 || !strings.Contains(errs, "--user") {
		t.Errorf("export bad --user: code=%d stderr=%q", code, errs)
	}

	file := filepath.Join(dir, "export.json")
	if code, _, errs := run(t, "export", "--storage-url", srcURL, "--user", fx.Owner.String(), "--out", file); code != 0 {
		t.Fatalf("export: code=%d stderr=%s", code, errs)
	}
	doc, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(doc, []byte("other@example.com")) || !bytes.Contains(doc, []byte("owner@example.com")) {
		t.Errorf("export holds the wrong user's data")
	}
	// Standard output gets the same bytes.
	if code, out, errs := run(t, "export", "--storage-url", srcURL, "--user", fx.Owner.String()); code != 0 || out != string(doc) {
		t.Errorf("stdout export differs from --out file: code=%d stderr=%s", code, errs)
	}

	// Import creates the schema in a fresh database and rebuilds projections.
	dstURL := "sqlite://" + filepath.Join(dir, "dst.db")
	code, out, errs := run(t, "import", "--storage-url", dstURL, "--in", file)
	if code != 0 || !strings.Contains(out, "imported user "+fx.Owner.String()) {
		t.Fatalf("import: code=%d out=%q stderr=%s", code, out, errs)
	}
	if code, out, _ := run(t, "export", "--storage-url", dstURL); code != 0 || out != string(doc) {
		t.Errorf("re-export (sole user, no --user) differs from the original")
	}

	// A second import into the same instance is refused and changes nothing.
	if code, _, errs := run(t, "import", "--storage-url", dstURL, "--in", file); code != 1 || !strings.Contains(errs, "already has users") {
		t.Errorf("second import: code=%d stderr=%q", code, errs)
	}
	if code, out, _ := run(t, "export", "--storage-url", dstURL); code != 0 || out != string(doc) {
		t.Errorf("a refused import changed the instance")
	}

	// A damaged file is refused with a message, not a stack trace.
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, string(doc[:len(doc)/2]))
	if code, _, errs := run(t, "import", "--storage-url", "sqlite://"+filepath.Join(dir, "bad.db"), "--in", bad); code != 1 || !strings.Contains(errs, "import:") {
		t.Errorf("truncated import: code=%d stderr=%q", code, errs)
	}
	if code, _, errs := run(t, "export", "--storage-url", "sqlite://"+filepath.Join(dir, "bad.db")); code != 1 || !strings.Contains(errs, "no users") {
		t.Errorf("export of a failed import's database: code=%d stderr=%q", code, errs)
	}
}
