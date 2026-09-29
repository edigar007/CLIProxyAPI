package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newCodexUsageTestHandler(t *testing.T, auths ...*coreauth.Auth) *Handler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	for _, a := range auths {
		a.EnsureIndex()
		if _, err := manager.Register(context.Background(), a); err != nil {
			t.Fatalf("register auth: %v", err)
		}
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
}

func codexAuth(id, email, accountID string, faithful bool) *coreauth.Auth {
	meta := map[string]any{
		"access_token": "at-" + id,
		"email":        email,
		"account_id":   accountID,
	}
	if faithful {
		meta["codex_faithful"] = true
	}
	return &coreauth.Auth{ID: id, FileName: id + ".json", Provider: "codex", Metadata: meta}
}

func doCodexUsage(t *testing.T, h *Handler, query string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-usage"+query, nil)
	h.GetCodexUsage(ctx)
	var payload map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	}
	return rec.Code, payload
}

func TestGetCodexUsageByAuthIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Errorf("missing Authorization header")
		}
		_, _ = w.Write([]byte(`{"plan_type":"team"}`))
	}))
	defer srv.Close()
	t.Setenv("CODEX_USAGE_URL", srv.URL)

	a := codexAuth("auth-a", "a@example.com", "acct-a", true)
	h := newCodexUsageTestHandler(t, a)

	code, payload := doCodexUsage(t, h, "?auth_index="+a.Index)
	if code != http.StatusOK {
		t.Fatalf("status = %d, payload=%v", code, payload)
	}
	if payload["auth_index"] != a.Index {
		t.Fatalf("auth_index = %v, want %q", payload["auth_index"], a.Index)
	}
	body, _ := payload["body"].(map[string]any)
	if body["plan_type"] != "team" {
		t.Fatalf("body = %v", payload["body"])
	}
}

func TestGetCodexUsageByEmailAndAccountID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("CODEX_USAGE_URL", srv.URL)

	h := newCodexUsageTestHandler(t,
		codexAuth("auth-a", "a@example.com", "acct-a", true),
		codexAuth("auth-b", "b@example.com", "acct-b", true),
	)

	if code, payload := doCodexUsage(t, h, "?email=b@example.com"); code != http.StatusOK || payload["email"] != "b@example.com" {
		t.Fatalf("email lookup failed: code=%d payload=%v", code, payload)
	}
	if code, payload := doCodexUsage(t, h, "?account_id=acct-a"); code != http.StatusOK || payload["email"] != "a@example.com" {
		t.Fatalf("account_id lookup failed: code=%d payload=%v", code, payload)
	}
}

func TestGetCodexUsageNotFound(t *testing.T) {
	h := newCodexUsageTestHandler(t, codexAuth("auth-a", "a@example.com", "acct-a", true))
	if code, _ := doCodexUsage(t, h, "?email=missing@example.com"); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestGetCodexUsageAmbiguous(t *testing.T) {
	h := newCodexUsageTestHandler(t,
		codexAuth("auth-a", "dup@example.com", "acct-a", true),
		codexAuth("auth-b", "dup@example.com", "acct-b", true),
	)
	if code, _ := doCodexUsage(t, h, "?email=dup@example.com"); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for ambiguous", code)
	}
}

func TestGetCodexUsageBatchOnlyFaithful(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("CODEX_USAGE_URL", srv.URL)

	h := newCodexUsageTestHandler(t,
		codexAuth("auth-faithful-1", "f1@example.com", "acct-1", true),
		codexAuth("auth-faithful-2", "f2@example.com", "acct-2", true),
		codexAuth("auth-plain", "plain@example.com", "acct-3", false),
	)

	code, payload := doCodexUsage(t, h, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	results, ok := payload["results"].([]any)
	if !ok {
		t.Fatalf("results missing: %v", payload)
	}
	if len(results) != 2 {
		t.Fatalf("batch returned %d results, want 2 (faithful only)", len(results))
	}
	for _, r := range results {
		entry := r.(map[string]any)
		if entry["email"] == "plain@example.com" {
			t.Fatalf("non-faithful config must be excluded from batch")
		}
	}
}

func TestGetCodexUsageBatchIsolatesErrors(t *testing.T) {
	// No CODEX_USAGE_URL stub and unreachable default endpoint is avoided by
	// pointing at a closed server so each fetch fails but batch still returns.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	t.Setenv("CODEX_USAGE_URL", srv.URL)

	h := newCodexUsageTestHandler(t,
		codexAuth("auth-faithful-1", "f1@example.com", "acct-1", true),
	)
	code, payload := doCodexUsage(t, h, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, batch should still return 200", code)
	}
	results, _ := payload["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	entry := results[0].(map[string]any)
	if _, hasErr := entry["error"]; !hasErr {
		t.Fatalf("expected per-account error field, got %v", entry)
	}
}
