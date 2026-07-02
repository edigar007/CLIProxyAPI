package management

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestUploadAuthFile_PreservesPriorityAttributes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	content := `{"type":"codex","email":"midai0530@gmail.com","priority":98}`

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "codex-midai0530@gmail.com-plus.json")
	if err != nil {
		t.Fatalf("failed to create multipart file: %v", err)
	}
	if _, err = part.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write multipart content: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if status, _ := payload["status"].(string); status != "ok" {
		t.Fatalf("expected status ok, got %#v", payload["status"])
	}

	auth, ok := manager.GetByID("codex-midai0530@gmail.com-plus.json")
	if !ok || auth == nil {
		t.Fatalf("expected uploaded auth record to exist")
	}
	if got := auth.Attributes["priority"]; got != "98" {
		t.Fatalf("priority attribute = %q, want %q", got, "98")
	}
	if got := auth.Metadata["priority"]; got != float64(98) {
		t.Fatalf("priority metadata = %#v, want 98", got)
	}
}

func TestImportCodexPersonalAccessToken_AcceptsTeamPlan(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/user-auth-credential/whoami" {
			t.Fatalf("path = %q, want /v1/user-auth-credential/whoami", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer at-team-token" {
			t.Fatalf("Authorization = %q, want Bearer at-team-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"team@example.com","chatgpt_user_id":"user-team","chatgpt_account_id":"workspace-team","chatgpt_plan_type":"team","chatgpt_account_is_fedramp":false}`))
	}))
	defer server.Close()
	t.Setenv("CODEX_AUTHAPI_BASE_URL", server.URL)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	body := bytes.NewBufferString(`{"access_token":"at-team-token"}`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/codex-personal-access-token", body)
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.ImportCodexPersonalAccessToken(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected import status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	expectedName := codex.CredentialFileName("team@example.com", "team", shortCodexAccountIDHash("workspace-team"), true)
	fullPath := filepath.Join(authDir, expectedName)
	if _, err := os.Stat(fullPath); err != nil {
		t.Fatalf("expected auth file %s to exist: %v", fullPath, err)
	}

	auth, ok := manager.GetByID(expectedName)
	if !ok || auth == nil {
		t.Fatalf("expected imported auth record to exist")
	}
	if got := auth.Metadata["personal_access_token"]; got != "at-team-token" {
		t.Fatalf("personal_access_token = %#v, want at-team-token", got)
	}
	if got := auth.Metadata["plan_type"]; got != "team" {
		t.Fatalf("plan_type = %#v, want team", got)
	}
	if got := auth.Metadata["email"]; got != "team@example.com" {
		t.Fatalf("email = %#v, want team@example.com", got)
	}
	if got := auth.Metadata["account_id"]; got != "workspace-team" {
		t.Fatalf("account_id = %#v, want workspace-team", got)
	}
}

func TestImportCodexPersonalAccessToken_RejectsUnsupportedPlan(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"pro@example.com","chatgpt_user_id":"user-pro","chatgpt_account_id":"workspace-pro","chatgpt_plan_type":"pro","chatgpt_account_is_fedramp":false}`))
	}))
	defer server.Close()
	t.Setenv("CODEX_AUTHAPI_BASE_URL", server.URL)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	body := bytes.NewBufferString(`{"access_token":"at-pro-token"}`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/codex-personal-access-token", body)
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.ImportCodexPersonalAccessToken(ctx)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected import status %d, got %d with body %s", http.StatusForbidden, rec.Code, rec.Body.String())
	}
	entries, err := os.ReadDir(authDir)
	if err != nil {
		t.Fatalf("failed to read auth dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no auth files to be created, got %d", len(entries))
	}
}
