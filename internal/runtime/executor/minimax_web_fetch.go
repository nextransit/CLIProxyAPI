package executor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var urlRegex = regexp.MustCompile(`https?://[^\s<>"{}|\\^` + "`" + `\[\]]+`)

// isLocalOrPrivateURL checks if the URL points to a local or private network
func isLocalOrPrivateURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true // Treat unparseable URLs as invalid
	}

	host := u.Hostname()
	if host == "" {
		return true
	}

	// Check for localhost
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}

	// Check if it's an IP address
	ip := net.ParseIP(host)
	if ip != nil {
		// Loopback
		if ip.IsLoopback() {
			return true
		}
		// Private IP ranges
		if ip.IsPrivate() || ip.IsUnspecified() {
			return true
		}
		// Link-local
		if ip.IsLinkLocal() {
			return true
		}
		return false
	}

	// For hostnames, check for private TLDs or known internal patterns
	lowerHost := strings.ToLower(host)
	if strings.HasSuffix(lowerHost, ".local") ||
		strings.HasSuffix(lowerHost, ".localhost") ||
		strings.HasSuffix(lowerHost, ".internal") ||
		strings.HasSuffix(lowerHost, ".private") {
		return true
	}

	return false
}

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
func fetchURLContent(rawURL string) (string, error) {
	// SSRF protection: reject local and private network addresses
	if isLocalOrPrivateURL(rawURL) {
		return "", fmt.Errorf("refusing to fetch URL from local or private network: %s", rawURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "CLIProxyAPI/1.0 (MiniMax Web Fetch Proxy)")

	// Create client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
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
