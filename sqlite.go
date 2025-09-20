package proserpina

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
)

// openSQLiteDB opens and configures an SQLite database handle.
func openSQLiteDB(dbFilepath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbFilepath)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", dbFilepath, err)
	}

	// Connection settings tuned for SQLite
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	return db, nil
}

// validateSQLBuffer checks basic constraints on the SQL buffer.
func validateSQLBuffer(path string, sqlBytes []byte) error {
	if len(bytes.TrimSpace(sqlBytes)) == 0 {
		return fmt.Errorf("query file %q is empty", path)
	}
	if !utf8.Valid(sqlBytes) {
		return fmt.Errorf("query file %q is not valid UTF-8; please save the file as UTF-8 (no BOM)", path)
	}
	return nil
}

// execSQLScript executes a SQL script on the given executor (db or tx).
type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func execSQLScript(ctx context.Context, exec sqlExecer, sqlBytes []byte, source string) error {
	if err := validateSQLBuffer(source, sqlBytes); err != nil {
		return err
	}
	_, err := exec.ExecContext(ctx, string(sqlBytes))
	if err != nil {
		return fmt.Errorf("execute SQL script %q: %w", source, err)
	}
	return nil
}

// RunQuery executes all SQL statements contained in queryFilepath against the SQLite
// database stored at dbFilepath. It returns nil on success, or a non-nil error on failure.
func RunQuery(dbFilepath string, queryFilepath string) error {
	log.Printf("runQuery: executing SQL script %q on database %q", queryFilepath, dbFilepath)

	sqlBytes, err := os.ReadFile(queryFilepath)
	if err != nil {
		return fmt.Errorf("read query file %q: %w", queryFilepath, err)
	}
	if err := validateSQLBuffer(queryFilepath, sqlBytes); err != nil {
		return err
	}
	log.Printf("runQuery: SQL script loaded (%d bytes)", len(sqlBytes))

	db, err := openSQLiteDB(dbFilepath)
	if err != nil {
		return err
	}
	defer db.Close()
	log.Printf("runQuery: database opened")

	// Context with timeout to avoid hanging forever on locks or long operations
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Apply pragmas once per connection open
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL;"); err != nil {
		return fmt.Errorf("set pragmas: %w", err)
	}
	log.Printf("runQuery: pragmas applied")

	// Execute within a transaction for atomicity
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	if err := execSQLScript(ctx, tx, sqlBytes, queryFilepath); err != nil {
		_ = tx.Rollback()
		return err
	}
	log.Printf("runQuery: SQL script executed successfully, committing")

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	log.Printf("runQuery: transaction committed")
	return nil
}

// classifyScript returns a weight to help order scripts when names are not properly numbered.
// Lower weights execute earlier (e.g., CREATE TABLE before INSERT).
func classifyScript(name string, content []byte) (weight int, numPrefix int) {
	// Numeric prefix if present (e.g., "02-", "10_", "1.sql")
	base := strings.TrimSpace(name)
	i := 0
	for i < len(base) && base[i] >= '0' && base[i] <= '9' {
		i++
	}
	num := -1
	if i > 0 {
		if v, err := strconv.Atoi(base[:i]); err == nil {
			num = v
		}
	}

	lower := strings.ToLower(string(content))
	switch {
	case strings.Contains(lower, "create table"):
		weight = 0
	case strings.Contains(lower, "create index") || strings.Contains(lower, "alter table") || strings.Contains(lower, "drop table"):
		weight = 1
	case strings.Contains(lower, "insert ") || strings.Contains(lower, "update ") || strings.Contains(lower, "delete "):
		weight = 2
	default:
		weight = 3
	}

	return weight, num
}

// RunQueryFolder loads all .sql files in the folder, orders them robustly, and executes them atomically.
func RunQueryFolder(dbFilepath string, queryFolderFilepath string) error {
	log.Printf("RunQueryFolder: executing SQL scripts in %q on database %q", queryFolderFilepath, dbFilepath)

	entries, err := os.ReadDir(queryFolderFilepath)
	if err != nil {
		return fmt.Errorf("read directory %q: %w", queryFolderFilepath, err)
	}

	type script struct {
		name      string
		path      string
		content   []byte
		weight    int
		numPrefix int
	}

	var scripts []script
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.ToLower(filepath.Ext(name)) != ".sql" {
			continue
		}
		p := filepath.Join(queryFolderFilepath, name)
		b, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read script %q: %w", p, err)
		}
		if err := validateSQLBuffer(p, b); err != nil {
			return err
		}
		w, n := classifyScript(name, b)
		scripts = append(scripts, script{name: name, path: p, content: b, weight: w, numPrefix: n})
	}

	if len(scripts) == 0 {
		log.Printf("RunQueryFolder: no .sql files found in %q; nothing to execute", queryFolderFilepath)
		return nil
	}

	// Robust sort:
	// 1) numeric prefix first when both files have a numeric prefix (ascending);
	// 2) schema-first by weight (e.g., CREATE TABLE before INSERT/UPDATE/DELETE);
	// 3) lexicographic name as a final tiebreaker.
	sort.SliceStable(scripts, func(i, j int) bool {
		ni, nj := scripts[i].numPrefix, scripts[j].numPrefix

		// If both have numeric prefixes, use them as the primary ordering
		if ni >= 0 && nj >= 0 && ni != nj {
			return ni < nj
		}

		// Otherwise, prefer schema-first
		if scripts[i].weight != scripts[j].weight {
			return scripts[i].weight < scripts[j].weight
		}

		// Final tiebreaker
		return scripts[i].name < scripts[j].name
	})

	db, err := openSQLiteDB(dbFilepath)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Apply pragmas once
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL;"); err != nil {
		return fmt.Errorf("set pragmas: %w", err)
	}

	// Single transaction for all scripts: atomic and efficient
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	for _, s := range scripts {
		log.Printf("RunQueryFolder: executing %s (weight=%d num=%d)", s.name, s.weight, s.numPrefix)
		if err := execSQLScript(ctx, tx, s.content, s.path); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	log.Printf("RunQueryFolder: completed executing %d scripts", len(scripts))
	return nil
}

func ExportTable(dbFile string, tableName string, csvFile string) error {
	// Input validation
	if dbFile == "" {
		return fmt.Errorf("database file path cannot be empty")
	}
	if tableName == "" {
		return fmt.Errorf("table name cannot be empty")
	}
	if csvFile == "" {
		return fmt.Errorf("CSV file path cannot be empty")
	}

	// Validate and quote the table name to avoid SQL injection via identifiers.
	safeTable, err := sanitizeIdentifier(tableName)
	if err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	// Open DB and prepare context
	db, err := openSQLiteDB(dbFile)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Apply pragmas for robustness
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL;"); err != nil {
		return fmt.Errorf("set pragmas: %w", err)
	}

	// Fetch column names in declaration order
	var columnNames []string
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s);", safeTable))
	if err != nil {
		return fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan table_info: %w", err)
		}
		columnNames = append(columnNames, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table_info: %w", err)
	}
	if len(columnNames) == 0 {
		return fmt.Errorf("table %q has no columns or does not exist", tableName)
	}

	// Prepare CSV file with UTF-8 BOM and CRLF line endings for Excel compatibility
	f, err := os.OpenFile(csvFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create csv file: %w", err)
	}
	defer f.Close()

	// Write BOM
	if _, err := f.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return fmt.Errorf("write BOM: %w", err)
	}

	w := csv.NewWriter(f)
	w.UseCRLF = true

	// Header with column names
	headers := make([]string, len(columnNames))
	copy(headers, columnNames)
	if err := w.Write(headers); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	// Build SELECT with quoted identifiers
	quotedCols := make([]string, len(columnNames))
	for i, c := range columnNames {
		qc, err := sanitizeIdentifier(c)
		if err != nil {
			return fmt.Errorf("invalid column name %q: %w", c, err)
		}
		quotedCols[i] = qc
	}
	selectSQL := fmt.Sprintf("SELECT %s FROM %s;", strings.Join(quotedCols, ", "), safeTable)

	rows, err = db.QueryContext(ctx, selectSQL)
	if err != nil {
		return fmt.Errorf("query rows: %w", err)
	}
	defer rows.Close()

	colCount := len(columnNames)
	for rows.Next() {
		destPtrs := make([]any, colCount)
		destVals := make([]any, colCount)
		for i := range destPtrs {
			destPtrs[i] = &destVals[i]
		}
		if err := rows.Scan(destPtrs...); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}

		record := make([]string, colCount)
		for i, v := range destVals {
			switch t := v.(type) {
			case nil:
				record[i] = ""
			case []byte:
				// TEXT/BLOB values
				if utf8.Valid(t) {
					record[i] = string(t)
				} else {
					// Hex-encode binary blobs to keep CSV valid and safe
					record[i] = "0x" + strings.ToLower(hex.EncodeToString(t))
				}
			case string:
				record[i] = t
			case int64, float64, bool:
				record[i] = fmt.Sprint(t)
			default:
				// Fallback to fmt.Sprint for any other driver-specific types
				record[i] = fmt.Sprint(t)
			}
		}

		if err := w.Write(record); err != nil {
			return fmt.Errorf("write record: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate rows: %w", err)
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}

	return nil
}
