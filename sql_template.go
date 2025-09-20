package proserpina

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed assets/sql/*.sql
var sqlFiles embed.FS

type loader struct {
	fs embed.FS
}

func newLoader() *loader {
	return &loader{fs: sqlFiles}
}

func (l *loader) readTemplate(name string) (string, error) {
	if !strings.HasSuffix(name, ".sql") {
		return "", fmt.Errorf("invalid file name: must end with .sql")
	}
	data, err := l.fs.ReadFile("assets/sql/" + name)
	if err != nil {
		return "", fmt.Errorf("failed to read SQL file %q: %w", name, err)
	}
	return string(data), nil
}

func (l *loader) prepareWithVariables(templateText string, vars map[string]string) (string, error) {
	// Build a sanitized map: quote identifiers by default; allow *_raw keys as validated unquoted.
	safe := make(map[string]string, len(vars))
	for k, v := range vars {
		if strings.HasSuffix(k, "_raw") {
			raw, err := sanitizeIdentifierRaw(v)
			if err != nil {
				return "", fmt.Errorf("unsafe identifier for %s: %q", k, v)
			}
			safe[k] = raw
			continue
		}
		quoted, err := sanitizeIdentifier(v)
		if err != nil {
			return "", fmt.Errorf("unsafe identifier for %s: %q", k, v)
		}
		safe[k] = quoted
	}

	tmpl, err := template.New("sql").Parse(templateText)
	if err != nil {
		return "", fmt.Errorf("failed to parse SQL template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, safe); err != nil {
		return "", fmt.Errorf("failed to execute SQL template: %w", err)
	}
	return sb.String(), nil
}

