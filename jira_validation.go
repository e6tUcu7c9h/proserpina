package proserpina

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Shared regular expressions for validation logic.
var (
	projectRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)             // JIRA project keys are uppercase
	jiraKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`) // JIRA issue key format
)

// validateProjectKey ensures JIRA project key follows expected format
func validateProjectKey(key string) error {
	if key == "" {
		return fmt.Errorf("project key cannot be empty")
	}
	if len(key) > 10 { // JIRA project key length limit
		return fmt.Errorf("project key %q too long (max 10 characters)", key)
	}
	if !projectRe.MatchString(key) {
		return fmt.Errorf("invalid JIRA project key %q: must be uppercase letters, digits, underscores only", key)
	}
	return nil
}

// validateJiraBaseURL ensures the base URL is properly formatted and uses HTTPS
func validateJiraBaseURL(baseURL string) error {
	if baseURL == "" {
		return fmt.Errorf("JIRA base URL cannot be empty")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid JIRA base URL %q: %w", baseURL, err)
	}

	if parsed.Host == "" {
		return fmt.Errorf("JIRA base URL missing host: %q", baseURL)
	}

	host := parsed.Hostname()
	isLoopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	isPrivate := isLoopback || strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "192.168.") || strings.HasSuffix(host, ".local")

	// Allow HTTP only for loopback (testing/local dev). For any non-loopback host, HTTPS is required.
	if parsed.Scheme == "http" && !isLoopback {
		return fmt.Errorf("JIRA base URL must use HTTPS for non-loopback hosts: %q", baseURL)
	}
	// Reject private/local networks when using HTTPS (security hardening).
	if parsed.Scheme == "https" && isPrivate {
		return fmt.Errorf("JIRA base URL cannot point to local/private networks: %q", baseURL)
	}
	// For non-loopback: enforce HTTPS
	if !isLoopback && parsed.Scheme != "https" {
		return fmt.Errorf("JIRA base URL must use HTTPS: %q", baseURL)
	}

	return nil
}

// validateHeaders ensures headers don't contain dangerous values
func validateHeaders(headers map[string]string) error {
	if headers == nil {
		return fmt.Errorf("headers cannot be nil")
	}

	for name, value := range headers {
		if name == "" {
			return fmt.Errorf("header name cannot be empty")
		}
		if len(name) > 200 {
			return fmt.Errorf("header name %q too long (max 200 characters)", name)
		}
		if len(value) > 8192 {
			return fmt.Errorf("header value for %q too long (max 8192 characters)", name)
		}
		// Prevent header injection
		if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q contains illegal characters", name)
		}
	}

	return nil
}

// validateJiraIssue performs security validation on JIRA issue data
func validateJiraIssue(issue *jiraIssue) error {
	if issue == nil {
		return fmt.Errorf("issue cannot be nil")
	}

	if issue.ID == "" {
		return fmt.Errorf("issue ID cannot be empty")
	}
	if len(issue.ID) > 20 {
		return fmt.Errorf("issue ID %q too long (max 20 characters)", issue.ID)
	}

	if issue.Key == "" {
		return fmt.Errorf("issue key cannot be empty")
	}
	if len(issue.Key) > 50 {
		return fmt.Errorf("issue key %q too long (max 50 characters)", issue.Key)
	}
	if !jiraKeyRe.MatchString(issue.Key) {
		return fmt.Errorf("invalid JIRA issue key format %q", issue.Key)
	}

	if issue.Fields == nil {
		return fmt.Errorf("issue fields cannot be nil")
	}

	return nil
}
