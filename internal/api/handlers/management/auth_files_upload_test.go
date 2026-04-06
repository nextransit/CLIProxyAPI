package management

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestUploadAuthFile_EmptyMultipartReturnsBadRequest(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, errCreatePart := writer.CreateFormFile("file", "token_empty.json")
	if errCreatePart != nil {
		t.Fatalf("create form file failed: %v", errCreatePart)
	}
	if _, errWritePart := part.Write(nil); errWritePart != nil {
		t.Fatalf("write form file failed: %v", errWritePart)
	}
	if errCloseWriter := writer.Close(); errCloseWriter != nil {
		t.Fatalf("close writer failed: %v", errCloseWriter)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	if _, errStat := os.Stat(filepath.Join(authDir, "token_empty.json")); !os.IsNotExist(errStat) {
		t.Fatalf("expected empty upload to leave no file, stat err: %v", errStat)
	}
}

func TestUploadAuthFile_ValidMultipartRegistersAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	payload := []byte(`{"type":"codex","email":"user@example.com"}`)
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, errCreatePart := writer.CreateFormFile("file", "token_valid.json")
	if errCreatePart != nil {
		t.Fatalf("create form file failed: %v", errCreatePart)
	}
	if _, errWritePart := part.Write(payload); errWritePart != nil {
		t.Fatalf("write form file failed: %v", errWritePart)
	}
	if errCloseWriter := writer.Close(); errCloseWriter != nil {
		t.Fatalf("close writer failed: %v", errCloseWriter)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}

	savedPath := filepath.Join(authDir, "token_valid.json")
	saved, errRead := os.ReadFile(savedPath)
	if errRead != nil {
		t.Fatalf("read saved file failed: %v", errRead)
	}
	if !bytes.Equal(saved, payload) {
		t.Fatalf("unexpected saved payload: %s", string(saved))
	}

	registered, ok := manager.GetByID("token_valid.json")
	if !ok || registered == nil {
		t.Fatalf("expected auth record to be registered")
	}
	if registered.Provider != "codex" {
		t.Fatalf("unexpected provider: %s", registered.Provider)
	}
}

func TestUploadAuthFile_EmptyRawBodyReturnsBadRequest(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files?name=raw_empty.json", bytes.NewReader(nil))
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	if _, errStat := os.Stat(filepath.Join(authDir, "raw_empty.json")); !os.IsNotExist(errStat) {
		t.Fatalf("expected empty upload to leave no file, stat err: %v", errStat)
	}
}
