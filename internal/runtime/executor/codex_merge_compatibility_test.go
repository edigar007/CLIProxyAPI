package executor

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexWebsocketIdentityConfusePreservesClientView(t *testing.T) {
	cfg := &config.Config{}
	cfg.Codex.IdentityConfuse = true
	cfg.Routing.SessionAffinity = true
	auth := &cliproxyauth.Auth{
		ID:         "identity-merge-test",
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "test-key", "base_url": "https://example.com"},
	}
	payload := []byte(`{"model":"gpt-6-astra","instructions":"test","input":[],"prompt_cache_key":"client-key"}`)
	executor := NewCodexWebsocketsExecutor(cfg)
	prepared, errPrepare := executor.prepareCodexWebsocketStream(context.Background(), auth,
		cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(prepared.clientBody, "prompt_cache_key").String(); got != "client-key" {
		t.Fatalf("client view changed: prompt_cache_key = %q", got)
	}
	upstreamKey := gjson.GetBytes(prepared.upstreamBody, "prompt_cache_key").String()
	if upstreamKey == "" || upstreamKey == "client-key" {
		t.Fatalf("upstream identity was not isolated: %q", upstreamKey)
	}
	if got := codexSessionHeaderValue(prepared.wsHeaders); got != upstreamKey {
		t.Fatalf("session header = %q, want upstream key %q", got, upstreamKey)
	}
}

func TestCodexIdentityConfuseRunsBeforeFinalPayloadRules(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-6-astra"}},
				Params: map[string]any{"prompt_cache_key": "configured-key"},
			}},
		},
	}
	cfg.Codex.IdentityConfuse = true
	cfg.Routing.SessionAffinity = true
	auth := &cliproxyauth.Auth{ID: "identity-payload-test", Provider: "codex"}
	payload := []byte(`{"model":"gpt-6-astra","input":[],"prompt_cache_key":"client-key"}`)
	req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
	ctx := helps.WithPayloadFinalizer(context.Background(),
		helps.NewPayloadFinalizer(cfg, "codex", req.Model, "codex", "", payload, req, opts))
	executor := NewCodexExecutor(cfg)
	httpReq, upstreamBody, _, errCache := executor.cacheHelperWithIdentity(ctx, opts.SourceFormat,
		"https://example.com/responses", auth, req, payload, payload)
	if errCache != nil {
		t.Fatal(errCache)
	}
	defer func() {
		if errClose := httpReq.Body.Close(); errClose != nil {
			t.Errorf("close request body: %v", errClose)
		}
	}()
	wireBody, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if got := gjson.GetBytes(wireBody, "prompt_cache_key").String(); got != "configured-key" {
		t.Fatalf("identity rewrite overrode final payload rule: %q", got)
	}
	if !bytes.Equal(wireBody, upstreamBody) {
		t.Fatal("serialized request differs from the finalized business payload")
	}
}
