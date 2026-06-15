package executor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractURLsFromContent(t *testing.T) {
	content := "Check out https://example.com/article for more info"
	urls := extractURLsFromContent(content)

	if len(urls) != 1 {
		t.Fatalf("expected 1 URL, got %d", len(urls))
	}
	if urls[0] != "https://example.com/article" {
		t.Errorf("unexpected URL: %s", urls[0])
	}
}

func TestExtractURLsFromContent_MultipleURLs(t *testing.T) {
	content := "See https://foo.com and http://bar.org/123"
	urls := extractURLsFromContent(content)

	if len(urls) != 2 {
		t.Fatalf("expected 2 URLs, got %d", len(urls))
	}
}

func TestExtractURLsFromContent_NoURLs(t *testing.T) {
	content := "No URLs here"
	urls := extractURLsFromContent(content)

	if len(urls) != 0 {
		t.Fatalf("expected 0 URLs, got %d", len(urls))
	}
}

func TestFetchURLContent_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Fetched content"))
	}))
	defer server.Close()

	content, err := fetchURLContentWithoutSSRFCheck(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "Fetched content" {
		t.Errorf("unexpected content: %s", content)
	}
}

func TestFetchURLContent_404Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := fetchURLContentWithoutSSRFCheck(server.URL)
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
	// Verify error message contains status code
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("expected error containing 'HTTP 404', got: %v", err)
	}
}

func TestReplaceURLsWithContent(t *testing.T) {
	content := "See https://example.com for details"
	replacement := "This is the fetched content"

	result := replaceURLsWithContent(content, "https://example.com", replacement)

	expected := "See This is the fetched content for details"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestIsLocalOrPrivateURL_Localhost(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"localhost", "http://localhost/path", true},
		{"127.0.0.1", "http://127.0.0.1/path", true},
		{"::1", "http://[::1]/path", true},
		{"private IP 192", "http://192.168.1.1/path", true},
		{"private IP 10", "http://10.0.0.1/path", true},
		{"private IP 172", "http://172.16.0.1/path", true},
		{".local domain", "http://server.local/path", true},
		{"public URL", "https://example.com/path", false},
		{"public URL no path", "https://google.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLocalOrPrivateURL(tt.url)
			if got != tt.want {
				t.Errorf("isLocalOrPrivateURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestFetchURLContent_LocalhostRejected(t *testing.T) {
	// This test verifies SSRF protection works
	_, err := fetchURLContent("http://127.0.0.1:9999/nonexistent")
	if err == nil {
		t.Fatal("expected error for localhost URL, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to fetch") {
		t.Errorf("expected error containing 'refusing to fetch', got: %v", err)
	}
}
