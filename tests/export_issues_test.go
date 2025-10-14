package proserpina_test

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lib "github.com/e6tUcu7c9h/proserpina"
)

type testIssue struct {
	ID     string                 `json:"id"`
	Key    string                 `json:"key"`
	Fields map[string]any         `json:"fields"`
}

type testResp struct {
	Issues []testIssue `json:"issues"`
	Total  int         `json:"total"`
}

func newJiraServer(t *testing.T, total int, projectKey string) *httptest.Server {
	t.Helper()
	// Build synthetic issues for pages of size 1000 as in the library
	pageSize := 1000
	issues := make([]testIssue, total)
	for i := 0; i < total; i++ {
		issues[i] = testIssue{
			ID:  fmt.Sprintf("%d", i+1),
			Key: fmt.Sprintf("%s-%d", projectKey, i+1),
			Fields: map[string]any{
				"summary": fmt.Sprintf("Issue %d", i+1),
				"custom":  i,
			},
		}
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// Validate headers pass-through (we will set one in tests)
		if got := r.Header.Get("X-Test-Auth"); got == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("missing header"))
			return
		}
		// Validate required query params
		if q.Get("expand") != "changelog" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte("expand must include changelog"))
			return
		}
		if q.Get("fields") != "*all" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte("fields must be *all"))
			return
		}
		startAt := 0
		fmt.Sscanf(q.Get("startAt"), "%d", &startAt)
		// Validate JQL
		jql := q.Get("jql")
		if jql == "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte("missing jql"))
			return
		}
		if !strings.HasPrefix(jql, "project=") && !strings.Contains(jql, projectKey) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte("unexpected jql"))
			return
		}
		// Compute slice according to page size
		end := startAt + pageSize
		if end > total {
			end = total
		}
		resp := testResp{Issues: issues[startAt:end], Total: total}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&resp)
	})
	return httptest.NewServer(h)
}

func TestExportIssues_EndToEnd_ProjectKey(t *testing.T) {
	server := newJiraServer(t, 1200, "DEMO")
	defer server.Close()

	db := t.TempDir()
	dbFile := filepath.Join(db, "jira.sqlite")
	headers := map[string]string{"X-Test-Auth": "ok"}

	if err := lib.ExportIssues(server.URL, headers, dbFile, "DEMO", "jira_issues", 4, ""); err != nil {
		t.Fatalf("ExportIssues failed: %v", err)
	}

	// Export to CSV and read back a few rows
	csvFile := filepath.Join(db, "out.csv")
	if err := lib.ExportTable(dbFile, "jira_issues", csvFile); err != nil {
		t.Fatalf("ExportTable failed: %v", err)
	}
	f, err := os.Open(csvFile)
	if err != nil {
		t.Fatalf("open csv: %v", err)
	}
	defer f.Close()
	cr := csv.NewReader(f)
	recs, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	// Header + 1200 records expected
	if len(recs) != 1201 {
		t.Fatalf("expected 1201 rows, got %d", len(recs))
	}
}

func TestExportIssues_JQLOutOfBandValidation(t *testing.T) {
	server := newJiraServer(t, 1, "DEMO")
	defer server.Close()

	db := t.TempDir()
	dbFile := filepath.Join(db, "jira.sqlite")
	headers := map[string]string{"X-Test-Auth": "ok"}

	// Invalid JQL (contains #)
	if err := lib.ExportIssues(server.URL, headers, dbFile, "DEMO", "jira_issues", 1, "status=Done #oops"); err == nil {
		t.Fatalf("expected invalid JQL error, got nil")
	}
}

func TestExportIssues_ServerErrorAborts(t *testing.T) {
	// Server that returns 500 for second page
	pageSize := 1000
	total := 1500
	projectKey := "DEMO"
	count := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		q := r.URL.Query()
		if r.Header.Get("X-Test-Auth") == "" {
			w.WriteHeader(401)
			return
		}
		startAt := 0
		fmt.Sscanf(q.Get("startAt"), "%d", &startAt)
		if startAt >= pageSize {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("boom"))
			return
		}
		issues := []testIssue{{ID: "1", Key: projectKey + "-1", Fields: map[string]any{"a": 1}}}
		resp := testResp{Issues: issues, Total: total}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&resp)
	})
	server := httptest.NewServer(h)
	defer server.Close()

	db := t.TempDir()
	dbFile := filepath.Join(db, "jira.sqlite")
	headers := map[string]string{"X-Test-Auth": "ok"}

	err := lib.ExportIssues(server.URL, headers, dbFile, projectKey, "jira_issues", 2, "")
	if err == nil || !strings.Contains(err.Error(), "aborting export") {
		t.Fatalf("expected aborting export error, got %v", err)
	}
}
