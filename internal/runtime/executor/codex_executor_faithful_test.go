package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// codexFaithfulTestContext builds a context carrying a gin.Context whose request
// headers mimic a real Codex CLI request, so faithful passthrough can forward them.
func codexFaithfulTestContext(t *testing.T) context.Context {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/backend-api/codex/responses", nil)
	req.Header.Set("User-Agent", "codex_cli_rs/0.133.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Session_id", "client-session-123")
	req.Header.Set("X-Client-Request-Id", "client-req-123")
	ginCtx := &gin.Context{Request: req}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestCodexExecutorFaithfulPassthroughForwardsClientRequest(t *testing.T) {
	var gotBody []byte
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "faithful-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token":   "at-faithful-token",
			"account_id":     "acct-999",
			"codex_faithful": true,
		},
	}

	// A codex-shaped request as the CLI would send it, including fields CPA would
	// normally strip and NO image_generation tool.
	payload := []byte(`{"model":"gpt-5.5","instructions":"","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true,"store":false,"previous_response_id":"resp_prev","prompt_cache_key":"client-key","reasoning":{"summary":"detailed"},"tools":[]}`)

	_, err := executor.Execute(codexFaithfulTestContext(t), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// No image_generation tool must be injected.
	tools := gjson.GetBytes(gotBody, "tools")
	for _, tool := range tools.Array() {
		if tool.Get("type").String() == "image_generation" {
			t.Fatalf("faithful mode must not inject image_generation tool; body=%s", string(gotBody))
		}
	}

	// Client-set fields must be preserved (not stripped).
	if gjson.GetBytes(gotBody, "previous_response_id").String() != "resp_prev" {
		t.Fatalf("previous_response_id should be preserved; body=%s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "prompt_cache_key").String() != "client-key" {
		t.Fatalf("prompt_cache_key should be preserved untouched; body=%s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "reasoning.summary").String() != "detailed" {
		t.Fatalf("reasoning.summary should be preserved untouched; body=%s", string(gotBody))
	}

	// Token swapped in; client identity headers preserved (not confused/overwritten).
	if got := gotHeaders.Get("Authorization"); got != "Bearer at-faithful-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := gotHeaders.Get("Chatgpt-Account-Id"); got != "acct-999" {
		t.Fatalf("Chatgpt-Account-Id = %q", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != "codex_cli_rs/0.133.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9" {
		t.Fatalf("User-Agent not preserved: %q", got)
	}
	if got := gotHeaders.Get("Originator"); got != "codex_cli_rs" {
		t.Fatalf("Originator not preserved: %q", got)
	}
	if got := gotHeaders.Get("Session_id"); got != "client-session-123" {
		t.Fatalf("Session_id should be the client value, got %q", got)
	}
	if got := gotHeaders.Get("X-Client-Request-Id"); got != "client-req-123" {
		t.Fatalf("X-Client-Request-Id should be the client value, got %q", got)
	}
}

func TestCodexExecutorNonFaithfulStillInjectsImageTool(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "normal-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "at-normal-token",
			"account_id":   "acct-111",
		},
	}

	payload := []byte(`{"model":"gpt-5.5","instructions":"","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Non-faithful path keeps the existing behavior: image_generation tool injected.
	found := false
	for _, tool := range gjson.GetBytes(gotBody, "tools").Array() {
		if tool.Get("type").String() == "image_generation" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("non-faithful mode should still inject image_generation tool; body=%s", string(gotBody))
	}
}
