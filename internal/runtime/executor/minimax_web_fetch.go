package executor

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var urlRegex = regexp.MustCompile(`https?://[^\s<>"{}|\\^` + "`" + `\[\]]+`)

// extractURLsFromContent finds all HTTP/HTTPS URLs in text content
func extractURLsFromContent(content string) []string {
	if content == "" {
		return nil
	}
	matches := urlRegex.FindAllString(content, -1)
	// Remove trailing punctuation from each URL
	var result []string
	for _, m := range matches {
		m = strings.TrimRight(m, ".,;:!?")
		result = append(result, m)
	}
	return result
}

// fetchURLContent retrieves the content at a given URL
func fetchURLContent(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "CLIProxyAPI/1.0 (MiniMax Web Fetch Proxy)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// replaceURLsWithContent replaces URLs in content with fetched content
func replaceURLsWithContent(content, url, replacement string) string {
	// Escape the replacement string for regex
	escaped := regexp.QuoteMeta(replacement)
	pattern := regexp.MustCompile(regexp.QuoteMeta(url) + `[^\s<>"{}|\\^` + "`" + `\[\]]*`)
	return pattern.ReplaceAllString(content, escaped)
}