package proserpina_test

import (
	"os"
	"path/filepath"
	"testing"

	lib "github.com/e6tUcu7c9h/proserpina"
)

func TestExportIssues_HeaderInjectionRejected(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db.sqlite")
	headers := map[string]string{"Bad\nName": "value"}
	if err := lib.ExportIssues("http://127.0.0.1", headers, db, "DEMO", "t", 1, ""); err == nil {
		t.Fatalf("expected header injection error")
	}
}

func TestExportTable_InvalidArgs(t *testing.T) {
	if err := lib.ExportTable("", "t", "x.csv"); err == nil {
		t.Fatalf("expected error for empty dbFile")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "db.sqlite")
	if err := os.WriteFile(filepath.Join(dir, "c.sql"), []byte(`CREATE TABLE t(x INT);`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := lib.RunQuery(db, filepath.Join(dir, "c.sql")); err != nil {
		t.Fatalf("RunQuery: %v", err)
	}
	if err := lib.ExportTable(db, "", filepath.Join(dir, "o.csv")); err == nil {
		t.Fatalf("expected error for empty table")
	}
	if err := lib.ExportTable(db, "t", ""); err == nil {
		t.Fatalf("expected error for empty csv path")
	}
}

func TestRunQueryFolder_InvalidUTF8(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "db.sqlite")
	p := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bad := filepath.Join(p, "bad.sql")
	// Write bytes that are not valid UTF-8
	if err := os.WriteFile(bad, []byte{0xff, 0xfe, 0xfd}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := lib.RunQueryFolder(db, p); err == nil {
		t.Fatalf("expected invalid UTF-8 error")
	}
}
