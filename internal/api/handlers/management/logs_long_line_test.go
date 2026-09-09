package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestGetLogs_AllowsLineLongerThanPreviousScannerLimit(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	logDir := t.TempDir()
	logFile := filepath.Join(logDir, defaultLogFileName)
	logContent := "[2026-09-06 12:46:34] [error] " + strings.Repeat("x", 8*1024*1024+1024) + "\n"
	if err := os.WriteFile(logFile, []byte(logContent), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{
		LoggingToFile: true,
	}, nil)
	handler.SetLogDirectory(logDir)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs", nil)

	handler.GetLogs(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(payload.Lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1", len(payload.Lines))
	}
	if !strings.Contains(payload.Lines[0], "[error]") {
		t.Fatalf("line = %q, want error level", payload.Lines[0][:min(len(payload.Lines[0]), 128)])
	}
}
