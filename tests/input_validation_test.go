package proserpina_test

import (
	"path/filepath"
	"strings"
	"testing"

	lib "github.com/e6tUcu7c9h/proserpina"
)

func TestExportIssues_InputValidation(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db.sqlite")
	headers := map[string]string{"X": "y"}

	// Empty URL
	if err := lib.ExportIssues("", headers, db, "DEMO", "t", 1, ""); err == nil {
		t.Fatalf("expected error for empty url")
	}
	// Invalid URL scheme for non-loopback (no host)
	if err := lib.ExportIssues("http://example.com", headers, db, "DEMO", "t", 1, ""); err == nil {
		t.Fatalf("expected error for insecure URL")
	}
	// Nil headers
	if err := lib.ExportIssues("http://127.0.0.1", nil, db, "DEMO", "t", 1, ""); err == nil {
		t.Fatalf("expected error for nil headers")
	}
	// Invalid project key when no JQL
	if err := lib.ExportIssues("http://127.0.0.1", headers, db, "bad-key", "t", 1, ""); err == nil {
		t.Fatalf("expected invalid project key error")
	}
	// Invalid table name
	if err := lib.ExportIssues("http://127.0.0.1", headers, db, "DEMO", "bad name", 1, ""); err == nil {
		t.Fatalf("expected invalid table name error")
	}
}

func TestExportIssues_JQLOK(t *testing.T) {
	// Validates that a simple JQL passes initial validation even if server isn't reachable
	db := filepath.Join(t.TempDir(), "db.sqlite")
	headers := map[string]string{"X": "y"}
	err := lib.ExportIssues("http://127.0.0.1:1", headers, db, "DEMO", "tab", 1, "project = DEMO ORDER BY created DESC")
	if err == nil {
		t.Fatalf("expected connection error; got nil")
	}
	if !strings.Contains(err.Error(), "connect") && !strings.Contains(strings.ToLower(err.Error()), "dial") {
		t.Fatalf("expected dial/connect error, got %v", err)
	}
}
