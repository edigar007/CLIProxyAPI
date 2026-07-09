package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchCodexUsageSendsCLIIdenticalRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type":"team"}`))
	}))
	defer server.Close()

	t.Setenv(codexUsageURLEnv, server.URL+"/backend-api/wham/usage")

	status, body, err := FetchCodexUsage(context.Background(), nil, "", "at-token", "acct-123", false)
	if err != nil {
		t.Fatalf("FetchCodexUsage error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if string(body) != `{"plan_type":"team"}` {
		t.Fatalf("body = %s", string(body))
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/backend-api/wham/usage" {
		t.Fatalf("path = %q", gotPath)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer at-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := gotHeaders.Get("ChatGPT-Account-Id"); got != "acct-123" {
		t.Fatalf("ChatGPT-Account-Id = %q", got)
	}
	if got := gotHeaders.Get("Originator"); got != codexUsageOriginator {
		t.Fatalf("Originator = %q, want %q", got, codexUsageOriginator)
	}
	if got := gotHeaders.Get("User-Agent"); got != codexUsageUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, codexUsageUserAgent)
	}
	if got := gotHeaders.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
	// Fedramp header must be absent for non-fedramp accounts.
	if got := gotHeaders.Get("X-OpenAI-Fedramp"); got != "" {
		t.Fatalf("X-OpenAI-Fedramp = %q, want empty", got)
	}
	// None of CPA's execution-path identity headers should be present.
	for _, h := range []string{"Session_id", "Thread-Id", "X-Client-Request-Id", "X-Codex-Window-Id", "X-Codex-Turn-Metadata"} {
		if v := gotHeaders.Get(h); v != "" {
			t.Fatalf("unexpected execution-path header %s = %q", h, v)
		}
	}
}

func TestFetchCodexUsageFedrampHeader(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	t.Setenv(codexUsageURLEnv, server.URL)

	if _, _, err := FetchCodexUsage(context.Background(), nil, "", "at-token", "", true); err != nil {
		t.Fatalf("FetchCodexUsage error: %v", err)
	}
	if got := gotHeaders.Get("X-OpenAI-Fedramp"); got != "true" {
		t.Fatalf("X-OpenAI-Fedramp = %q, want true", got)
	}
	// Empty account id must not set the header.
	if got := gotHeaders.Get("ChatGPT-Account-Id"); got != "" {
		t.Fatalf("ChatGPT-Account-Id = %q, want empty", got)
	}
}

func TestFetchCodexUsageEmptyToken(t *testing.T) {
	if _, _, err := FetchCodexUsage(context.Background(), nil, "", "  ", "acct", false); err == nil {
		t.Fatal("expected error for empty token")
	}
}
