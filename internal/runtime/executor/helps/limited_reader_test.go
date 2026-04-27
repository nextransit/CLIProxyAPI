package helps

import (
	"errors"
	"strings"
	"testing"
)

func TestReadAllWithLimitReturnsDataWithinLimit(t *testing.T) {
	data, err := readAllWithLimit(strings.NewReader("abc"), 3)
	if err != nil {
		t.Fatalf("readAllWithLimit returned error: %v", err)
	}
	if string(data) != "abc" {
		t.Fatalf("data = %q, want %q", string(data), "abc")
	}
}

func TestReadAllWithLimitReportsTooLarge(t *testing.T) {
	data, err := readAllWithLimit(strings.NewReader("abcd"), 3)
	if !errors.Is(err, ErrHTTPResponseBodyTooLarge) {
		t.Fatalf("error = %v, want ErrHTTPResponseBodyTooLarge", err)
	}
	if string(data) != "abc" {
		t.Fatalf("data = %q, want truncated prefix %q", string(data), "abc")
	}
}
