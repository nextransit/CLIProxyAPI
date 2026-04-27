package helps

import (
	"errors"
	"fmt"
	"io"
)

// MaxHTTPResponseBodyBytes caps upstream HTTP response body reads (50 MiB).
// Prevents unbounded allocation from unexpectedly large provider payloads.
const MaxHTTPResponseBodyBytes int64 = 50 << 20

// ErrHTTPResponseBodyTooLarge indicates an upstream HTTP response exceeded the configured cap.
var ErrHTTPResponseBodyTooLarge = errors.New("upstream HTTP response body exceeds limit")

// LimitedReadAll reads an upstream HTTP response body with a hard size cap.
func LimitedReadAll(r io.Reader) ([]byte, error) {
	return readAllWithLimit(r, MaxHTTPResponseBodyBytes)
}

func readAllWithLimit(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return data, err
	}
	if int64(len(data)) > limit {
		return data[:limit], fmt.Errorf("%w: %d bytes", ErrHTTPResponseBodyTooLarge, limit)
	}
	return data, nil
}
