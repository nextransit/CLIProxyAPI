package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestListAuthFiles_PaginatesAndCapsPageSizeToThousand(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)

	for i := 1; i <= 120; i++ {
		name := fmt.Sprintf("auth-%03d.json", i)
		path := filepath.Join(authDir, name)
		if err := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}

		record := &coreauth.Auth{
			ID:       fmt.Sprintf("auth-%03d", i),
			FileName: name,
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": path,
			},
			Metadata: map[string]any{
				"email": fmt.Sprintf("user-%03d@example.com", i),
			},
		}
		if _, err := manager.Register(context.Background(), record); err != nil {
			t.Fatalf("Register(%q) error = %v", name, err)
		}
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files?page=1&page_size=2000", nil)

	handler.ListAuthFiles(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	files, ok := payload["files"].([]any)
	if !ok {
		t.Fatalf("payload.files type = %T, want []any", payload["files"])
	}
	if len(files) != 120 {
		t.Fatalf("len(files) = %d, want 120", len(files))
	}
	if got := int(payload["page"].(float64)); got != 1 {
		t.Fatalf("page = %d, want 1", got)
	}
	if got := int(payload["page_size"].(float64)); got != 1000 {
		t.Fatalf("page_size = %d, want 1000", got)
	}
	if got := int(payload["total"].(float64)); got != 120 {
		t.Fatalf("total = %d, want 120", got)
	}
	if got := int(payload["total_pages"].(float64)); got != 1 {
		t.Fatalf("total_pages = %d, want 1", got)
	}
}

func TestListAuthFiles_ExposesLastErrorFieldsForSearch(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)

	name := "auth-problem.json"
	path := filepath.Join(authDir, name)
	if err := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}

	record := &coreauth.Auth{
		ID:            "auth-problem",
		FileName:      name,
		Provider:      "codex",
		Status:        coreauth.StatusError,
		StatusMessage: "quota exhausted",
		LastError: &coreauth.Error{
			Code:       "usage_limit_reached",
			Message:    `token refresh failed with status 401: {"error":{"code":"refresh_token_reused","message":"Your refresh token has already been used to generate a new access token. Please try signing in again."}}`,
			HTTPStatus: http.StatusUnauthorized,
		},
		Attributes: map[string]string{
			"path": path,
		},
	}
	if _, err := manager.Register(context.Background(), record); err != nil {
		t.Fatalf("Register(%q) error = %v", name, err)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	handler.ListAuthFiles(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	files, ok := payload["files"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("payload.files = %#v, want single entry", payload["files"])
	}

	entry, ok := files[0].(map[string]any)
	if !ok {
		t.Fatalf("files[0] type = %T, want map[string]any", files[0])
	}

	if got := entry["error_code"]; got != "usage_limit_reached" {
		t.Fatalf("error_code = %#v, want usage_limit_reached", got)
	}
	if got := entry["error_http_status"]; int(got.(float64)) != http.StatusUnauthorized {
		t.Fatalf("error_http_status = %#v, want %d", got, http.StatusUnauthorized)
	}
	if got := fmt.Sprint(entry["error_message"]); !strings.Contains(got, "refresh_token_reused") {
		t.Fatalf("error_message = %q, want refresh_token_reused", got)
	}

	lastError, ok := entry["last_error"].(map[string]any)
	if !ok {
		t.Fatalf("last_error type = %T, want map[string]any", entry["last_error"])
	}
	if got := lastError["code"]; got != "usage_limit_reached" {
		t.Fatalf("last_error.code = %#v, want usage_limit_reached", got)
	}
	if got := fmt.Sprint(lastError["message"]); !strings.Contains(got, "refresh_token_reused") {
		t.Fatalf("last_error.message = %q, want refresh_token_reused", got)
	}
	if got := lastError["http_status"]; int(got.(float64)) != http.StatusUnauthorized {
		t.Fatalf("last_error.http_status = %#v, want %d", got, http.StatusUnauthorized)
	}
}
