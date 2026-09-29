package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// codexFaithfulTestContext builds a context carrying a gin.Context whose request
// headers mimic a real Codex CLI request, so faithful passthrough can forward them.
func codexFaithfulTestContext(t *testing.T) context.Context {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/backend-api/codex/responses", nil)
	req.Header.Set("User-Agent", "codex_cli_rs/0.148.0 (Windows 10; x86_64)")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Session-Id", "client-session-123")
	req.Header.Set("Thread-Id", "client-thread-123")
	req.Header.Set("X-Client-Request-Id", "client-req-123")
	req.Header.Set("X-Codex-Window-Id", "client-window-123")
	req.Header.Set("X-Codex-Turn-Metadata", `{"session_id":"client-session-123","thread_id":"client-thread-123","turn_id":"turn-123","request_kind":"turn"}`)
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

	// This mirrors the ResponsesApiRequest serialized by codex-cli 0.148.0.
	// It intentionally has no image_generation tool.
	payload := []byte(`{"model":"gpt-5.6-sol","instructions":"follow the user request","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[],"tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"high","summary":"detailed"},"store":false,"stream":true,"stream_options":{"reasoning_summary_delivery":"sequential_cutoff"},"include":["reasoning.encrypted_content"],"service_tier":"priority","prompt_cache_key":"client-key","text":{"verbosity":"medium"},"client_metadata":{"x-codex-installation-id":"install-123","session_id":"client-session-123","thread_id":"client-thread-123","turn_id":"turn-123","x-codex-window-id":"client-window-123","x-codex-turn-metadata":"{\"session_id\":\"client-session-123\",\"thread_id\":\"client-thread-123\",\"turn_id\":\"turn-123\",\"request_kind\":\"turn\"}"}}`)

	_, err := executor.Execute(codexFaithfulTestContext(t), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Fatalf("faithful body changed\n got: %s\nwant: %s", string(gotBody), string(payload))
	}

	// No image_generation tool must be injected.
	tools := gjson.GetBytes(gotBody, "tools")
	for _, tool := range tools.Array() {
		if tool.Get("type").String() == "image_generation" {
			t.Fatalf("faithful mode must not inject image_generation tool; body=%s", string(gotBody))
		}
	}

	// Current Codex fields must be preserved, including canonical client metadata.
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
	if got := gotHeaders.Get("User-Agent"); got != "codex_cli_rs/0.148.0 (Windows 10; x86_64)" {
		t.Fatalf("User-Agent not preserved: %q", got)
	}
	if got := gotHeaders.Get("Originator"); got != "codex_cli_rs" {
		t.Fatalf("Originator not preserved: %q", got)
	}
	if got := gotHeaders.Get("Session-Id"); got != "client-session-123" {
		t.Fatalf("Session-Id should be the client value, got %q", got)
	}
	if got := gotHeaders.Get("X-Client-Request-Id"); got != "client-req-123" {
		t.Fatalf("X-Client-Request-Id should be the client value, got %q", got)
	}
	if got := gotHeaders.Get(codexRoutingHintHeader); got != "model=gpt-5.6-sol;tier=priority" {
		t.Fatalf("%s = %q", codexRoutingHintHeader, got)
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

func TestCodexFaithfulHeadersDropDecodedContentEncoding(t *testing.T) {
	ctx := codexFaithfulTestContext(t)
	ginCtx := ctx.Value("gin").(*gin.Context)
	ginCtx.Request.Header.Set("Content-Encoding", "zstd")
	ginCtx.Request.Header.Set("Content-Length", "123")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/responses", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Encoding", "zstd")
	request.Header.Set("Content-Length", "123")

	auth := &cliproxyauth.Auth{Metadata: map[string]any{"account_id": "acct-1"}}
	applyCodexFaithfulHeaders(request, auth, "token")
	if got := request.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty after body decoding", got)
	}
	if got := request.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want empty for rewritten request body", got)
	}
}

func TestCodexAutoExecutorFaithfulHTTPFallbackDropsWebsocketHandshakeHeaders(t *testing.T) {
	var gotRequest bool
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		gotRequest = true
		if got := req.Header.Values("Upgrade"); len(got) > 0 {
			return nil, fmt.Errorf("http2: invalid Upgrade request header: %q", got)
		}
		for _, key := range []string{
			"Connection",
			"Sec-WebSocket-Key",
			"Sec-WebSocket-Version",
			"Sec-WebSocket-Protocol",
			"Sec-WebSocket-Extensions",
		} {
			if got := req.Header.Get(key); got != "" {
				t.Fatalf("%s = %q, want empty on HTTP fallback", key, got)
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n",
			)),
			Request: req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)

	executor := NewCodexAutoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "faithful-http-fallback",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":   "at-faithful-token",
			"account_id":     "acct-999",
			"codex_faithful": true,
		},
	}
	headers := http.Header{}
	headers.Set("Connection", "Upgrade")
	headers.Set("Upgrade", "websocket")
	headers.Set("Sec-WebSocket-Key", "downstream-key")
	headers.Set("Sec-WebSocket-Version", "13")
	headers.Set("Sec-WebSocket-Protocol", "responses")
	headers.Set("Sec-WebSocket-Extensions", "permessage-deflate")

	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
		Headers:      headers,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream HTTP fallback error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}
	if !gotRequest {
		t.Fatal("HTTP fallback request was not sent")
	}
}

func TestCodexFaithfulWebsocketHeadersPreserveCanonicalClientIdentity(t *testing.T) {
	ctx := codexFaithfulTestContext(t)
	ginCtx := ctx.Value("gin").(*gin.Context)
	ginCtx.Request.Header.Set("Session-Id", "session-canonical")
	ginCtx.Request.Header.Set("Thread-Id", "thread-1")
	ginCtx.Request.Header.Set("X-Codex-Window-Id", "window-1")
	ginCtx.Request.Header.Set("X-Codex-Turn-Metadata", `{"turn_id":"turn-1"}`)

	headers := applyCodexFaithfulWebsocketHeaders(ctx, http.Header{}, &cliproxyauth.Auth{
		Metadata: map[string]any{"account_id": "acct-1"},
	}, "token")
	if got := headers.Get("Session-Id"); got != "session-canonical" {
		t.Fatalf("Session-Id = %q, want canonical client value", got)
	}
	if got := headers.Get("Thread-Id"); got != "thread-1" {
		t.Fatalf("Thread-Id = %q, want client value", got)
	}
	if got := headers.Get("X-Codex-Window-Id"); got != "window-1" {
		t.Fatalf("X-Codex-Window-Id = %q, want client value", got)
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got == "" {
		t.Fatal("X-Codex-Turn-Metadata should be preserved")
	}
}

func TestCodexFaithfulWebsocketHeadersDropDownstreamHandshakeHeaders(t *testing.T) {
	ctx := codexFaithfulTestContext(t)
	ginCtx := ctx.Value("gin").(*gin.Context)
	ginCtx.Request.Header.Set("Connection", "Upgrade")
	ginCtx.Request.Header.Set("Upgrade", "websocket")
	ginCtx.Request.Header.Set("Sec-WebSocket-Key", "downstream-key")
	ginCtx.Request.Header.Set("Sec-WebSocket-Version", "13")
	ginCtx.Request.Header.Set("Sec-WebSocket-Protocol", "responses")
	ginCtx.Request.Header.Set("Sec-WebSocket-Extensions", "permessage-deflate")

	headers := applyCodexFaithfulWebsocketHeaders(ctx, http.Header{}, &cliproxyauth.Auth{}, "token")
	for _, key := range []string{
		"Connection",
		"Upgrade",
		"Sec-WebSocket-Key",
		"Sec-WebSocket-Version",
		"Sec-WebSocket-Protocol",
		"Sec-WebSocket-Extensions",
	} {
		if got := headers.Get(key); got != "" {
			t.Fatalf("%s = %q, want empty before upstream websocket dial", key, got)
		}
	}
}

func TestApplyCodexFaithfulRoutingHintPreservesClientValueAndSkipsAPIKeys(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","service_tier":"priority"}`)
	headers := http.Header{codexRoutingHintHeader: []string{"client-value"}}
	applyCodexFaithfulRoutingHint(headers, &cliproxyauth.Auth{}, body)
	if got := headers.Get(codexRoutingHintHeader); got != "client-value" {
		t.Fatalf("client routing hint changed to %q", got)
	}

	apiKeyHeaders := http.Header{}
	applyCodexFaithfulRoutingHint(apiKeyHeaders, &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test"}}, body)
	if got := apiKeyHeaders.Get(codexRoutingHintHeader); got != "" {
		t.Fatalf("API-key request routing hint = %q, want empty", got)
	}
}

func TestBuildCodexWebsocketRequestBodyFaithfulPreservesCurrentEnvelope(t *testing.T) {
	payload := []byte(`{"type":"response.create","model":"gpt-5.6-sol","previous_response_id":"resp-prev","input":[{"type":"function_call_output","call_id":"call-1","output":"ok"}],"stream":true,"client_metadata":{"session_id":"session-1","thread_id":"thread-1","x-codex-ws-stream-request-start-ms":"1787200000000"}}`)
	got := buildCodexWebsocketRequestBody(payload, true)
	if !bytes.Equal(got, payload) {
		t.Fatalf("faithful websocket body changed\n got: %s\nwant: %s", string(got), string(payload))
	}
}

func TestBuildCodexWebsocketRequestBodyFaithfulAddsMissingCreateType(t *testing.T) {
	payload := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"hello"}],"stream":true}`)
	got := buildCodexWebsocketRequestBody(payload, true)
	if gotType := gjson.GetBytes(got, "type").String(); gotType != "response.create" {
		t.Fatalf("faithful websocket type = %q, want response.create; body=%s", gotType, string(got))
	}
}
