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

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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
		// Link-local (IPv4: 169.254.0.0/16, IPv6: fe80::/10)
		if ip4 := ip.To4(); ip4 != nil && ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		if ip.IsLinkLocalUnicast() {
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

// shouldRouteWebSearchToOpenRouter checks if web search should be routed to OpenRouter
// based on provider configuration. Returns true for "openrouter" provider.
func shouldRouteWebSearchToOpenRouter(provider string) bool {
	return provider == "openrouter"
}

// containsWebSearchTool checks if the tools array contains a web_search tool
// by name or by type prefix (e.g., "web_search_20250305").
func containsWebSearchTool(tools []byte) bool {
	if !gjson.ValidBytes(tools) {
		return false
	}
	toolsArr := gjson.GetBytes(tools, "tools").Array()
	for _, t := range toolsArr {
		name := t.Get("name").String()
		if name == "web_search" {
			return true
		}
		tp := t.Get("type").String()
		if strings.HasPrefix(tp, "web_search") {
			return true
		}
	}
	return false
}

// routeWebSearchToOpenRouter transforms a request to route web_search to OpenRouter.
// It removes web_search tool from the request and adds metadata to indicate routing.
// Returns modified payload and true if routing was applied.
func routeWebSearchToOpenRouter(payload []byte) ([]byte, bool) {
	if !containsWebSearchTool(payload) {
		return payload, false
	}

	// Remove web_search tool from tools array
	var newTools []interface{}
	tools := gjson.GetBytes(payload, "tools")
	for _, t := range tools.Array() {
		name := t.Get("name").String()
		tp := t.Get("type").String()
		if name == "web_search" || strings.HasPrefix(tp, "web_search") {
			continue
		}
		newTools = append(newTools, t.Value())
	}

	if len(newTools) == 0 {
		if out, err := sjson.DeleteBytes(payload, "tools"); err != nil {
			log.Debugf("routeWebSearchToOpenRouter: failed to delete tools: %v", err)
			return payload, false
		} else {
			payload = out
		}
	} else {
		if out, err := sjson.SetBytes(payload, "tools", newTools); err != nil {
			log.Debugf("routeWebSearchToOpenRouter: failed to set tools: %v", err)
			return payload, false
		} else {
			payload = out
		}
	}

	// Add metadata to indicate OpenRouter routing
	if out, err := sjson.SetBytes(payload, "_meta.route_web_search_to_openrouter", true); err != nil {
		log.Debugf("routeWebSearchToOpenRouter: failed to set metadata: %v", err)
		return payload, false
	} else {
		payload = out
	}

	return payload, true
}
