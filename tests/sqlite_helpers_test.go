package proserpina_test

import (
	"os"
	"path/filepath"
	"testing"

	lib "github.com/e6tUcu7c9h/proserpina"
)

func TestRunQueryAndRunQueryFolder(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "test.sqlite")

	// Single RunQuery: create a table
	createFile := filepath.Join(dir, "001_create.sql")
	if err := os.WriteFile(createFile, []byte(`CREATE TABLE t(x INTEGER);
`), 0o600); err != nil {
		t.Fatalf("write create: %v", err)
	}
	if err := lib.RunQuery(db, createFile); err != nil {
		t.Fatalf("RunQuery failed: %v", err)
	}

	// Prepare a folder with ordered scripts: insert then add another row via separate file
	fdir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(fdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fdir, "10_insert.sql"), []byte(`INSERT INTO t(x) VALUES (1);`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fdir, "20_insert.sql"), []byte(`INSERT INTO t(x) VALUES (2);`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := lib.RunQueryFolder(db, fdir); err != nil {
		t.Fatalf("RunQueryFolder failed: %v", err)
	}

	// Export table to CSV and verify 2 rows
	csvFile := filepath.Join(dir, "t.csv")
	if err := lib.ExportTable(db, "t", csvFile); err != nil {
		t.Fatalf("ExportTable failed: %v", err)
	}
	data, err := os.ReadFile(csvFile)
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	// Header + 2 rows
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 3 { // includes trailing newline
		t.Fatalf("expected 3 lines in csv (header+2 rows), got %d", lines)
	}
}

func TestRunQuery_EmptyFileFails(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "test.sqlite")
	f := filepath.Join(dir, "empty.sql")
	if err := os.WriteFile(f, []byte("\n\n\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := lib.RunQuery(db, f); err == nil {
		t.Fatalf("expected error for empty SQL file")
	}
}
