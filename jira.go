package proserpina

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	pageSize = 1000
)

type jiraResponse struct {
	Issues []jiraIssue `json:"issues"`
	Total  int         `json:"total"`
}

type jiraIssue struct {
	ID     string                 `json:"id"`
	Key    string                 `json:"key"`
	Fields map[string]interface{} `json:"fields"`
}


// fetchIssues performs a single paginated fetch.
func fetchIssues(jiraBaseURL, projectKey string, headers map[string]string, startAt int, jqlOverride string) (jiraResponse, error) {
	log.Printf("Fetching issues from %d", startAt)

	// Prepare request parameters once; request will be rebuilt per attempt to attach a fresh context.
	buildRequest := func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, jiraBaseURL, nil)
		if err != nil {
			return nil, err
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "proserpina/1.0")
		}
		// Query parameters
		q := req.URL.Query()
		if jqlOverride != "" {
			safeJQL, vErr := validateAndSanitizeJQL(jqlOverride)
			if vErr != nil {
				return nil, fmt.Errorf("invalid JQL override: %w", vErr)
			}
			q.Set("jql", safeJQL)
		} else {
			q.Set("jql", fmt.Sprintf("project=%s", projectKey))
		}
		q.Set("startAt", strconv.Itoa(startAt))
		q.Set("maxResults", strconv.Itoa(pageSize))
		q.Set("fields", "*all")
		// Always enable changelog
		q.Set("expand", "changelog")
		req.URL.RawQuery = q.Encode()
		return req, nil
	}

	client := &http.Client{} // no timeout to accommodate slow Jira API

	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		req, err := buildRequest(ctx)
		if err != nil {
			cancel()
			return jiraResponse{}, err
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			if attempt < 3 {
				time.Sleep(time.Duration(50*attempt) * time.Millisecond)
				continue
			}
			return jiraResponse{}, err
		}

		// Read and handle response
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			cancel()
			// Retry on 429/5xx
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				if attempt < 3 {
					time.Sleep(time.Duration(50*attempt) * time.Millisecond)
					continue
				}
				return jiraResponse{}, fmt.Errorf("JIRA API %s returned status %d: %s", req.URL.String(), resp.StatusCode, string(bodySnippet))
			}
			// Non-retryable status
			return jiraResponse{}, fmt.Errorf("JIRA API %s returned status %d: %s", req.URL.String(), resp.StatusCode, string(bodySnippet))
		}

		var jr jiraResponse
		decErr := json.NewDecoder(resp.Body).Decode(&jr)
		resp.Body.Close()
		cancel()
		if decErr != nil {
			if attempt < 3 {
				time.Sleep(time.Duration(50*attempt) * time.Millisecond)
				continue
			}
			return jiraResponse{}, decErr
		}
		return jr, nil
	}

	return jiraResponse{}, fmt.Errorf("unexpected fetch state")
}

func saveIssuesToDB(issues []jiraIssue, dbFile string, tableName string) error {
	if len(issues) == 0 {
		log.Printf("No issues to save to database")
		return nil
	}
	if dbFile == "" {
		return fmt.Errorf("database file path cannot be empty")
	}
	if len(dbFile) > 4096 {
		return fmt.Errorf("database file path too long (max 4096 characters)")
	}
	// Validate the raw table name early; templates will apply quoting.
	if _, err := sanitizeIdentifierRaw(tableName); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	log.Printf("Saving %d issues to DB file %s in table %s", len(issues), dbFile, tableName)

	db, err := openSQLiteDB(dbFile)
	if err != nil {
		return fmt.Errorf("failed to open database file %q: %w", dbFile, err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			log.Printf("Warning: failed to close database: %v", closeErr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 10000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA temp_store = memory",
		"PRAGMA mmap_size = 268435456", // 256MB
		"PRAGMA cache_size = 10000",
	}

	for _, pragma := range pragmas {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("set pragma %q: %w", pragma, err)
		}
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				log.Printf("Warning: failed to rollback transaction: %v", rollbackErr)
			}
		}
	}()

	loader := newLoader()
	tableVars := map[string]string{
		"tablename":     tableName,
		"tablename_raw": tableName,
	}

	createTmpl, err := loader.readTemplate("create_jira_table.sql")
	if err != nil {
		return fmt.Errorf("could not load jira table creation sql: %w", err)
	}
	createTableSQL, err := loader.prepareWithVariables(createTmpl, tableVars)
	if err != nil {
		return fmt.Errorf("could not prepare jira table creation sql: %w", err)
	}
	if _, err := tx.ExecContext(ctx, createTableSQL); err != nil {
		return fmt.Errorf("failed to execute statement %q: %w", createTableSQL, err)
	}

	updTmpl, err := loader.readTemplate("soft_delete_current.sql")
	if err != nil {
		return fmt.Errorf("could not load soft delete sql: %w", err)
	}
	updSQL, err := loader.prepareWithVariables(updTmpl, tableVars)
	if err != nil {
		return fmt.Errorf("could not prepare soft delete sql: %w", err)
	}
	updStmt, err := tx.PrepareContext(ctx, updSQL)
	if err != nil {
		return fmt.Errorf("prepare update statement: %w", err)
	}
	defer func() {
		if closeErr := updStmt.Close(); closeErr != nil {
			log.Printf("Warning: failed to close update statement: %v", closeErr)
		}
	}()

	insTmpl, err := loader.readTemplate("insert_jira_row.sql")
	if err != nil {
		return fmt.Errorf("could not load jira insert row sql: %w", err)
	}
	insSQL, err := loader.prepareWithVariables(insTmpl, tableVars)
	if err != nil {
		return fmt.Errorf("could not prepare jira insert row sql: %w", err)
	}
	insStmt, err := tx.PrepareContext(ctx, insSQL)
	if err != nil {
		return fmt.Errorf("prepare insert statement: %w", err)
	}
	defer func() {
		if closeErr := insStmt.Close(); closeErr != nil {
			log.Printf("Warning: failed to close prepared statement: %v", closeErr)
		}
	}()

	// Process issues
	for i, issue := range issues {
		// Additional validation per issue
		if err := validateJiraIssue(&issue); err != nil {
			return fmt.Errorf("invalid issue at index %d: %w", i, err)
		}

		fieldsJSON, err := json.Marshal(issue.Fields)
		if err != nil {
			return fmt.Errorf("marshal fields for issue %s: %w", issue.ID, err)
		}

		// Check JSON size limit (prevent DoS)
		if len(fieldsJSON) > 1024*1024 { // 1MB limit per issue
			return fmt.Errorf("issue %s fields too large: %d bytes (max 1MB)", issue.ID, len(fieldsJSON))
		}

		// Mark previous current row (if any) for this key as deleted
		if _, err = updStmt.ExecContext(ctx, issue.Key); err != nil {
			return fmt.Errorf("mark previous version deleted for key %s: %w", issue.Key, err)
		}

		// Insert the new current version
		if _, err = insStmt.ExecContext(ctx, issue.Key, string(fieldsJSON)); err != nil {
			return fmt.Errorf("insert issue %s into table %q: %w", issue.Key, tableName, err)
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true

	log.Printf("Successfully saved %d issues to database", len(issues))
	return nil
}

func worker(wg *sync.WaitGroup, jiraBaseURL, projectKey string, headers map[string]string, jobs <-chan int, results chan<- jiraResponse, errCount *int64, jqlOverride string) {
	defer wg.Done()
	for startAt := range jobs {
		jr, err := fetchIssues(jiraBaseURL, projectKey, headers, startAt, jqlOverride)
		if err != nil {
			atomic.AddInt64(errCount, 1)
			log.Printf("Error fetching issues at startAt %d: %v", startAt, err)
			// Do not send a dummy result; let the collector only merge successful batches.
			continue
		}
		results <- jr
	}
}

// ExportIssues exports Jira issues into a SQLite database table.
// Parameters:
// - jiraBaseURL: full search API URL endpoint (e.g., https://your-jira/rest/api/2/search)
// - headers: HTTP headers for authentication
// - dbFile: path to SQLite file
// - projectKey: Jira project key (used if jqlOverride is empty)
// - tableName: destination table
// - concurrent: number of concurrent workers (if <= 0, a safe default is used)
// - jqlOverride: custom JQL; when empty, defaults to "project=<projectKey>"
func ExportIssues(jiraBaseURL string, headers map[string]string, dbFile string, projectKey string, tableName string, concurrent int, jqlOverride string) error {
	// Input validation
	if jiraBaseURL == "" {
		return fmt.Errorf("JIRA base URL cannot be empty")
	}
	if err := validateJiraBaseURL(jiraBaseURL); err != nil {
		return err
	}
	if err := validateHeaders(headers); err != nil {
		return err
	}
	if jqlOverride == "" {
		if err := validateProjectKey(projectKey); err != nil {
			return err
		}
	} else {
		if _, err := validateAndSanitizeJQL(jqlOverride); err != nil {
			return fmt.Errorf("invalid JQL override: %w", err)
		}
	}
	if dbFile == "" {
		return fmt.Errorf("database file path cannot be empty")
	}
	if tableName == "" {
		return fmt.Errorf("table name cannot be empty")
	}
	if _, err := sanitizeIdentifierRaw(tableName); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	if concurrent <= 0 {
		concurrent = 8
	}

	log.Printf("Exporting issues. project: %s, concurrent: %d, changelog: %t, jqlOverride: %t", projectKey, concurrent, true, jqlOverride != "")

	var wg sync.WaitGroup
	var errCount int64

	numWorkers := concurrent
	jobs := make(chan int, numWorkers)
	results := make(chan jiraResponse, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker(&wg, jiraBaseURL, projectKey, headers, jobs, results, &errCount, jqlOverride)
	}

	// Fetch first page to know total issues
	firstResponse, err := fetchIssues(jiraBaseURL, projectKey, headers, 0, jqlOverride)
	if err != nil {
		return fmt.Errorf("failed to fetch first page: %w", err)
	}

	totalIssues := firstResponse.Total
	log.Printf("Total number of issues: %d", totalIssues)

	allIssues := make([]jiraIssue, 0, totalIssues)
	allIssues = append(allIssues, firstResponse.Issues...)

	for startAt := pageSize; startAt < totalIssues; startAt += pageSize {
		jobs <- startAt
	}
	close(jobs)

	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for response := range results {
			allIssues = append(allIssues, response.Issues...)
		}
	}()

	wg.Wait()
	close(results)
	<-collectorDone

	finalCount := len(allIssues)
	if failed := atomic.LoadInt64(&errCount); failed > 0 {
		return fmt.Errorf("aborting export: %d request(s) failed; refusing to produce incomplete dataset", failed)
	}

	dir := filepath.Dir(dbFile)
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return fmt.Errorf("failed to ensure database directory: %w", mkErr)
	}

	if err = saveIssuesToDB(allIssues, dbFile, tableName); err != nil {
		return fmt.Errorf("failed to save issues to database: %w", err)
	}

	log.Printf("Exported %d issues to %s.", finalCount, dbFile)
	return nil
}
