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

// TODO: Configuration schema validation

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

func fetchIssues(jiraBaseURL, projectKey string, headers map[string]string, startAt int) (jiraResponse, error) {
	log.Printf("Fetching issues from %d", startAt)
	client := &http.Client{}
	req, err := http.NewRequest("GET", jiraBaseURL, nil)
	if err != nil {
		return jiraResponse{}, err
	}

	// Set headers for authentication
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	// Set query parameters
	q := req.URL.Query()
	q.Add("jql", fmt.Sprintf("project=%s", projectKey))
	q.Add("startAt", strconv.Itoa(startAt))
	q.Add("maxResults", strconv.Itoa(pageSize))
	q.Add("fields", "*all")
	req.URL.RawQuery = q.Encode()

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return jiraResponse{}, err
	}
	defer resp.Body.Close()

	// Fail on non-2xx to avoid incomplete datasets
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return jiraResponse{}, fmt.Errorf("JIRA API %s returned status %d: %s", req.URL.String(), resp.StatusCode, string(bodySnippet))
	}

	// Decode the response
	var jr jiraResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return jiraResponse{}, err
	}

	return jr, nil
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

func worker(wg *sync.WaitGroup, jiraBaseURL, projectKey string, headers map[string]string, jobs <-chan int, results chan<- jiraResponse, errCount *int64) {
	defer wg.Done()
	for startAt := range jobs {
		jr, err := fetchIssues(jiraBaseURL, projectKey, headers, startAt)
		if err != nil {
			atomic.AddInt64(errCount, 1)
			log.Printf("Error fetching issues at startAt %d: %v", startAt, err)
			// Do not send a dummy result; let the collector only merge successful batches.
			continue
		}
		results <- jr
	}
}

func ExportIssues(jiraBaseURL string, headers map[string]string, dbFile string, projectKey string, tableName string) {
	// Input validation
	if jiraBaseURL == "" {
		log.Printf("Error: JIRA base URL cannot be empty")
		return
	}
	if err := validateJiraBaseURL(jiraBaseURL); err != nil {
		log.Printf("Error: %v", err)
		return
	}
	if err := validateHeaders(headers); err != nil {
		log.Printf("Error: %v", err)
		return
	}
	if err := validateProjectKey(projectKey); err != nil {
		log.Printf("Error: %v", err)
		return
	}
	if dbFile == "" {
		log.Printf("Error: database file path cannot be empty")
		return
	}
	if tableName == "" {
		log.Printf("Error: table name cannot be empty")
		return
	}
	if _, err := sanitizeIdentifierRaw(tableName); err != nil {
		log.Printf("Error: invalid table name: %v", err)
		return
	}

	log.Printf("Exporting issues for project key: %s", projectKey)

	var wg sync.WaitGroup
	var errCount int64

	numWorkers := 24
	jobs := make(chan int, numWorkers)             // Channel for startAt pagination values
	results := make(chan jiraResponse, numWorkers) // Channel for the results from API calls

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker(&wg, jiraBaseURL, projectKey, headers, jobs, results, &errCount)
	}

	// Fetch first page to know total issues
	firstResponse, err := fetchIssues(jiraBaseURL, projectKey, headers, 0)
	if err != nil {
		log.Printf("Failed to fetch first page: %v", err)
		return
	}

	totalIssues := firstResponse.Total
	log.Printf("Total number of issues: %d", totalIssues)

	// Pre-allocate slice for all issues
	allIssues := make([]jiraIssue, 0, totalIssues)
	allIssues = append(allIssues, firstResponse.Issues...)

	for startAt := pageSize; startAt < totalIssues; startAt += pageSize {
		jobs <- startAt
	}
	close(jobs) // Close jobs channel after sending all jobs

	// Collect results
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for response := range results {
			allIssues = append(allIssues, response.Issues...)
		}
	}()

	wg.Wait()
	close(results) // Close results channel when all workers are done
	<-collectorDone

	// Final validation before saving
	finalCount := len(allIssues)

	if finalCount == 0 {
		log.Printf("Warning: No issues found for project %s", projectKey)
		return
	}

	// Abort if any request failed to avoid an incomplete dataset
	if failed := atomic.LoadInt64(&errCount); failed > 0 {
		log.Printf("Aborting export: %d request(s) failed; refusing to produce incomplete dataset", failed)
		return
	}

	log.Printf("Collected %d issues, writing database directly to %s", finalCount, dbFile)

	// Ensure the destination directory exists.
	dir := filepath.Dir(dbFile)
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		log.Printf("Failed to ensure database directory: %v", mkErr)
		return
	}

	err = saveIssuesToDB(allIssues, dbFile, tableName)
	if err != nil {
		log.Printf("Failed to save issues to database: %v", err)
		return
	}

	log.Println("Jira issues export completed successfully.")
}
