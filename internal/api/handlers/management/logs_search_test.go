package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestGetLogs_SearchFiltersByKeyword(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	logDir := t.TempDir()
	logFile := filepath.Join(logDir, defaultLogFileName)
	logContent := "" +
		"[2026-03-27 10:00:00] [info] startup complete\n" +
		"[2026-03-27 10:01:00] [error] {\"error\":{\"message\":\"Your authentication token has been invalidated. Please try signing in again.\",\"type\":\"invalid_request_error\",\"code\":\"token_invalidated\",\"param\":null},\"status\":401}\n" +
		"[2026-03-27 10:02:00] [warn] retry scheduled\n"
	if err := os.WriteFile(logFile, []byte(logContent), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{
		LoggingToFile: true,
	}, nil)
	handler.SetLogDirectory(logDir)

	query := url.Values{}
	query.Set("search", "TOKEN_INVALIDATED")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs?"+query.Encode(), nil)

	handler.GetLogs(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	lines, ok := payload["lines"].([]any)
	if !ok {
		t.Fatalf("payload.lines type = %T, want []any", payload["lines"])
	}
	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1", len(lines))
	}

	line, ok := lines[0].(string)
	if !ok {
		t.Fatalf("lines[0] type = %T, want string", lines[0])
	}
	if want := "\"code\":\"token_invalidated\""; !contains(line, want) {
		t.Fatalf("line = %q, want substring %q", line, want)
	}
	if got := int(payload["line-count"].(float64)); got != 1 {
		t.Fatalf("line-count = %d, want 1", got)
	}
}

func TestGetLogs_SearchSupportsUsageLimitReachedAlias(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	logDir := t.TempDir()
	logFile := filepath.Join(logDir, defaultLogFileName)
	logContent := "" +
		"[2026-03-27 10:00:00] [info] startup complete\n" +
		"[2026-03-27 10:01:00] [debug] [model_registry.go:649] Marked model gpt-5.4 as quota exceeded for client abby@example.com.json\n" +
		"[2026-03-27 10:01:00] [debug] [model_registry.go:696] Suspended client abby@example.com.json for model gpt-5.4: quota\n"
	if err := os.WriteFile(logFile, []byte(logContent), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{
		LoggingToFile: true,
	}, nil)
	handler.SetLogDirectory(logDir)

	query := url.Values{}
	query.Set("search", "usage_limit_reached")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs?"+query.Encode(), nil)

	handler.GetLogs(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	lines, ok := payload["lines"].([]any)
	if !ok {
		t.Fatalf("payload.lines type = %T, want []any", payload["lines"])
	}
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}

	if got := int(payload["line-count"].(float64)); got != 2 {
		t.Fatalf("line-count = %d, want 2", got)
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})())
}
