package proserpina

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

// TODO: Run DLL after database initialization
// TODO: Configuration schema validation
// TODO: Docstrings

const (
	pageSize = 1000
)

type JiraResponse struct {
	Issues []JiraIssue `json:"issues"`
	Total  int         `json:"total"`
}

type JiraIssue struct {
	ID     string                 `json:"id"`
	Key    string                 `json:"key"`
	Fields map[string]interface{} `json:"fields"`
}

func fetchIssues(jiraBaseURL, projectKey string, headers map[string]string, startAt int) (JiraResponse, error) {
	log.Printf("Fetching issues from %d", startAt)
	client := &http.Client{}
	req, err := http.NewRequest("GET", jiraBaseURL, nil)
	if err != nil {
		return JiraResponse{}, err
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
		return JiraResponse{}, err
	}
	defer resp.Body.Close()

	// Decode the response
	var jiraResponse JiraResponse
	if err := json.NewDecoder(resp.Body).Decode(&jiraResponse); err != nil {
		return JiraResponse{}, err
	}

	return jiraResponse, nil
}

func saveIssuesToCSV(issues []JiraIssue, csvFile string) error {
	log.Printf("Saving issues to CSV file: %s", csvFile)
	file, err := os.Create(csvFile)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write CSV headers
	headers := []string{"ID", "Key", "Fields"}
	if err := writer.Write(headers); err != nil {
		log.Fatalf("Failed to write CSV headers: %v", err)
		return err
	}

	// Write issue data
	for _, issue := range issues {
		fieldsJSON, _ := json.Marshal(issue.Fields)
		record := []string{issue.ID, issue.Key, string(fieldsJSON)}
		if err := writer.Write(record); err != nil {
			log.Fatalf("Failed to write data in CSV file: %v", err)
			return err
		}
	}
	return nil
}

func saveIssuesToDB(issues []JiraIssue, dbFile string, tableName string) error {
	log.Printf("Saving issues to DB file %s in table %s.", dbFile, tableName)

	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		log.Fatalf("Failed to open database file: %v", err)
		return err
	}
	defer db.Close()

	// Create table if it doesn't exist
	createTableSQL := fmt.Sprintf(`
	CREATE TABLE IF NOT EXISTS %s (
		id TEXT PRIMARY KEY,
		key TEXT,
		fields TEXT
	);`, tableName)
	_, err = db.Exec(createTableSQL)
	if err != nil {
		log.Fatalf("Failed to create the table in the database: %v", err)
		return err
	}

	// Insert issues into the table
	insertSQL := fmt.Sprintf(`INSERT OR REPLACE INTO %s (id, key, fields) VALUES (?, ?, ?)`, tableName)
	for _, issue := range issues {
		fieldsJSON, _ := json.Marshal(issue.Fields)
		_, err = db.Exec(insertSQL, issue.ID, issue.Key, string(fieldsJSON))
		if err != nil {
			log.Fatalf("Could not insert values in the table: %v", err)
			return err
		}
	}
	return nil
}

func worker(wg *sync.WaitGroup, jiraBaseURL, projectKey string, headers map[string]string, jobs <-chan int, results chan<- JiraResponse) {
	defer wg.Done()
	for startAt := range jobs {
		jiraResp, err := fetchIssues(jiraBaseURL, projectKey, headers, startAt)
		if err != nil {
			log.Printf("Error fetching issues at startAt %d: %v", startAt, err)
			continue
		}
		results <- jiraResp
	}
}

func ExportIssues(jiraBaseURL string, headers map[string]string, dbFile string, projectKey string, csvFile string, tableName string) {
	log.Printf("Exporting issues for project key: %s", projectKey)
	var wg sync.WaitGroup
	jobs := make(chan int, 10)             // Channel for startAt pagination values
	results := make(chan JiraResponse, 10) // Channel for the results from API calls

	// Start workers
	numWorkers := 12
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker(&wg, jiraBaseURL, projectKey, headers, jobs, results)
	}

	// Fetch first page to know total issues
	firstResponse, err := fetchIssues(jiraBaseURL, projectKey, headers, 0)
	if err != nil {
		log.Fatalf("Failed to fetch first page: %v", err)
	}

	totalIssues := firstResponse.Total
	log.Printf("Total number of issues: %d", totalIssues)

	// Send pagination jobs to the workers
	go func() {
		for startAt := 0; startAt < totalIssues; startAt += pageSize {
			jobs <- startAt
		}
		close(jobs) // Close jobs channel after sending all jobs
	}()

	var allIssues []JiraIssue

	// Collect results
	go func() {
		for response := range results {
			allIssues = append(allIssues, response.Issues...)
		}
	}()

	wg.Wait()
	close(results) // Close results channel when all workers are done

	// Save to CSV and database
	if err := saveIssuesToCSV(allIssues, csvFile); err != nil {
		log.Fatalf("Failed to save issues to CSV: %v", err)
	}

	if err := saveIssuesToDB(allIssues, dbFile, tableName); err != nil {
		log.Fatalf("Failed to save issues to database: %v", err)
	}

	log.Println("Jira issues export completed successfully.")
}
