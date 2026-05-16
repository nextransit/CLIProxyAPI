package executor

import (
	"net/http"
	"net/http/httptest"
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

	content, err := fetchURLContent(server.URL)
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

	_, err := fetchURLContent(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 404 should return nil error but empty content
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