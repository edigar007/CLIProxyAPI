package codex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadPersonalAccessTokenMetadata(t *testing.T) {
	const token = "at-test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/user-auth-credential/whoami" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected whoami request: method=%s path=%s authorized=%v", r.Method, r.URL.Path, r.Header.Get("Authorization") == "Bearer "+token)
		}
		_, _ = fmt.Fprint(w, `{"email":"test@example.com","chatgpt_account_id":"account-123","chatgpt_plan_type":"team"}`)
	}))
	defer server.Close()
	meta, err := loadPersonalAccessTokenMetadata(context.Background(), server.Client(), server.URL+"/v1/user-auth-credential/whoami", token)
	if err != nil || meta.ChatgptAccountID != "account-123" {
		t.Fatalf("whoami account ID = %v, err=%v", meta, err)
	}
}

func TestLoadPersonalAccessTokenMetadataDoesNotExposeResponse(t *testing.T) {
	const token = "at-test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, "Authorization: Bearer "+token)
	}))
	defer server.Close()
	_, err := loadPersonalAccessTokenMetadata(context.Background(), server.Client(), server.URL, token)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), token) {
		t.Fatalf("validation error leaked credentials or omitted status: %v", err)
	}
}

func TestNormalizePersonalAccessToken(t *testing.T) {
	got, err := NormalizePersonalAccessToken("  at-test-secret  ")
	if err != nil || got != "at-test-secret" {
		t.Fatalf("normalized PAT = %q err=%v", got, err)
	}
	for _, invalid := range []string{"", "at-", "other-secret"} {
		if _, err := NormalizePersonalAccessToken(invalid); err == nil || strings.Contains(err.Error(), invalid+"secret") {
			t.Fatalf("invalid token accepted or exposed: %v", err)
		}
	}
}
