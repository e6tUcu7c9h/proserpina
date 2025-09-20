package proserpina

import (
	"fmt"
	"regexp"
)

// sanitizeIdentifierRaw validates an SQL identifier and returns the raw (unquoted) form.
func sanitizeIdentifierRaw(name string) (string, error) {
	var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	if name == "" {
		return "", fmt.Errorf("identifier cannot be empty")
	}
	if len(name) > 63 {
		return "", fmt.Errorf("identifier %q too long (max 63 characters)", name)
	}
	if !identRe.MatchString(name) {
		return "", fmt.Errorf("invalid SQL identifier %q", name)
	}
	return name, nil
}

// sanitizeIdentifier validates and returns a safely quoted identifier.
// The quoted form is suitable for use wherever an identifier is referenced in SQL.
func sanitizeIdentifier(name string) (string, error) {
	raw, err := sanitizeIdentifierRaw(name)
	if err != nil {
		return "", err
	}
	// Double-quote for SQL identifiers (SQLite, PostgreSQL style)
	return `"` + raw + `"`, nil
}
