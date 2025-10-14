package proserpina_test

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	lib "github.com/e6tUcu7c9h/proserpina"
)

func TestRunQuery_InvalidSQL(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db.sqlite")
	f := filepath.Join(t.TempDir(), "bad.sql")
	// This will fail at execution time
	if err := os.WriteFile(f, []byte("THIS IS NOT SQL"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := lib.RunQuery(db, f); err == nil {
		t.Fatalf("expected execution error for invalid SQL")
	}
}

func TestExportTable_TableDoesNotExist(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db.sqlite")
	// Create an empty DB
	if err := lib.RunQuery(db, filepath.Join(".", "go.mod")); err == nil {
		// If somehow succeeds, continue; but typically RunQuery will fail reading non-sql path.
	}
	if err := lib.ExportTable(db, "nope", filepath.Join(t.TempDir(), "out.csv")); err == nil {
		t.Fatalf("expected error for non-existent table")
	}
}

func TestExportIssues_RejectsPrivateHTTPS(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db.sqlite")
	if err := lib.ExportIssues("https://192.168.1.2", map[string]string{"X": "y"}, db, "DEMO", "t", 1, ""); err == nil {
		t.Fatalf("expected rejection for private HTTPS base URL")
	}
}

func TestExportIssues_InvalidJSONFromServer(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{")) // malformed
	})
	server := httptest.NewUnstartedServer(h)
	// Allow HTTP without TLS
	server.Config.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	server.Start()
	defer server.Close()

	db := filepath.Join(t.TempDir(), "db.sqlite")
	err := lib.ExportIssues(server.URL, map[string]string{"X": "y"}, db, "DEMO", "t", 1, "")
	if err == nil {
		t.Fatalf("expected error due to malformed JSON response")
	}
}
