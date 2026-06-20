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

func TestGetLogFiles_SearchFiltersByFilename(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	logDir := t.TempDir()
	files := map[string]string{
		"main.log":                    "current log",
		"main.log.1":                  "rotated log",
		"error-v1-chat-2026-test.log": "request error log",
		"v1-chat-2026-plain.log":      "request log",
		"ignore-me.tmp":               "tmp",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(logDir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", name, err)
		}
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{
		LoggingToFile: true,
	}, nil)
	handler.SetLogDirectory(logDir)

	query := url.Values{}
	query.Set("search", "error-v1-chat")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/log-files?"+query.Encode(), nil)

	handler.GetLogFiles(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload struct {
		Files []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"files"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if payload.Total != 1 {
		t.Fatalf("total = %d, want 1", payload.Total)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(payload.Files))
	}
	if payload.Files[0].Name != "error-v1-chat-2026-test.log" {
		t.Fatalf("files[0].Name = %q, want error-v1-chat-2026-test.log", payload.Files[0].Name)
	}
	if payload.Files[0].Type != "request-error" {
		t.Fatalf("files[0].Type = %q, want request-error", payload.Files[0].Type)
	}
}

func TestDownloadLogFile_ReturnsManagedLogFile(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	logDir := t.TempDir()
	const name = "main.log"
	const content = "hello from main log"
	if err := os.WriteFile(filepath.Join(logDir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{
		LoggingToFile: true,
	}, nil)
	handler.SetLogDirectory(logDir)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "name", Value: name}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/log-files/"+name, nil)

	handler.DownloadLogFile(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Body.String(); got != content {
		t.Fatalf("body = %q, want %q", got, content)
	}
}

func TestGetRequestLogDetailByID_ReturnsFullContent(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	logDir := t.TempDir()
	const requestID = "deadbeef"
	const name = "v1-responses-2026-06-20T112602-deadbeef.log"
	const content = "=== REQUEST INFO ===\nMethod: POST\n=== API REQUEST 1 ===\nBody:\n{\"model\":\"deepseek-v4-flash\"}\n=== API RESPONSE 1 ===\nStatus: 400\n"
	if err := os.WriteFile(filepath.Join(logDir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{
		LoggingToFile: true,
	}, nil)
	handler.SetLogDirectory(logDir)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: requestID}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-log-detail-by-id/"+requestID, nil)

	handler.GetRequestLogDetailByID(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if payload.ID != requestID {
		t.Fatalf("id = %q, want %q", payload.ID, requestID)
	}
	if payload.Name != name {
		t.Fatalf("name = %q, want %q", payload.Name, name)
	}
	if payload.Size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", payload.Size, len(content))
	}
	if payload.Content != content {
		t.Fatalf("content = %q, want %q", payload.Content, content)
	}
}
