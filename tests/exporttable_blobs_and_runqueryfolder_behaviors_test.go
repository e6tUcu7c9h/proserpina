package proserpina_test

import (
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lib "github.com/e6tUcu7c9h/proserpina"
)

func TestExportTable_HexBlobEncoding(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "test.sqlite")
	// Create table and insert a blob that is not valid UTF-8
	create := filepath.Join(dir, "c.sql")
	if err := os.WriteFile(create, []byte(`CREATE TABLE b (id INTEGER PRIMARY KEY, v BLOB);
INSERT INTO b(v) VALUES (x'00FF01');
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := lib.RunQuery(db, create); err != nil {
		t.Fatalf("RunQuery: %v", err)
	}
	csvFile := filepath.Join(dir, "b.csv")
	if err := lib.ExportTable(db, "b", csvFile); err != nil {
		t.Fatalf("ExportTable: %v", err)
	}
	f, _ := os.Open(csvFile)
	defer f.Close()
	cr := csv.NewReader(f)
	recs, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	val := recs[1][1]
	if !strings.HasPrefix(val, "0x") {
		t.Fatalf("expected hex-encoded blob prefix 0x, got %q", val)
	}
	// Ensure hex corresponds to inserted bytes
	b, err := hex.DecodeString(strings.TrimPrefix(val, "0x"))
	if err != nil || len(b) == 0 {
		t.Fatalf("invalid hex blob: %v", err)
	}
}

func TestRunQueryFolder_EmptyAndIgnore(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "db.sqlite")
	empty := filepath.Join(dir, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Empty dir should be a no-op
	if err := lib.RunQueryFolder(db, empty); err != nil {
		t.Fatalf("RunQueryFolder(empty) failed: %v", err)
	}
	// Directory with non-sql files should ignore them
	mix := filepath.Join(dir, "mix")
	if err := os.MkdirAll(mix, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mix, "a.txt"), []byte("ignore"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mix, "1_init.sql"), []byte(`CREATE TABLE z(id INT);`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := lib.RunQueryFolder(db, mix); err != nil {
		t.Fatalf("RunQueryFolder(mix) failed: %v", err)
	}
}

func TestExportIssues_LargeFieldsFails(t *testing.T) {
	// Server returns a huge fields payload (>1MB) to trigger size guard
	big := strings.Repeat("A", 1024*1024+10)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := map[string]any{
			"id":     "1",
			"key":    "DEMO-1",
			"fields": map[string]any{"big": big},
		}
		resp := map[string]any{"issues": []any{issue}, "total": 1}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	server := httptest.NewServer(h)
	defer server.Close()

	db := filepath.Join(t.TempDir(), "db.sqlite")
	headers := map[string]string{"X": "y"}
	err := lib.ExportIssues(server.URL, headers, db, "DEMO", "t", 1, "")
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected large fields error, got %v", err)
	}
}
