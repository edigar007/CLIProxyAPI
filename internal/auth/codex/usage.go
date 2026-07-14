// Package codex provides authentication and token management for OpenAI Codex.
// This file implements a read-only usage/quota lookup that mirrors exactly what
// the Codex CLI sends, deliberately avoiding any of the request-shaping applied
// on the execution (/responses) path so it cannot diverge from real CLI traffic.
package codex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const (
	// codexUsageURL is the endpoint the Codex CLI/TUI queries for rate-limit and
	// quota information. It can be overridden for testing via CODEX_USAGE_URL.
	codexUsageURL    = "https://chatgpt.com/backend-api/wham/usage"
	codexUsageURLEnv = "CODEX_USAGE_URL"

	// These identify the request as originating from the Codex CLI, matching the
	// values the real client sends. Keeping them identical is the whole point:
	// the usage read must be indistinguishable from Codex CLI traffic.
	codexUsageUserAgent  = "codex_cli_rs/0.133.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9"
	codexUsageOriginator = "codex_cli_rs"

	// codexUsageTimeout bounds the credential-scoped read. This is a short,
	// pre-connection credential acquisition style call and is intentionally
	// allowed to carry a timeout.
	codexUsageTimeout = 60 * time.Second
)

// ApplyFaithfulCodexIdentityHeaders sets the codex-CLI-faithful identity headers
// on an outgoing request so it is indistinguishable from real Codex CLI traffic.
// It sets Originator, User-Agent, ChatGPT-Account-Id, and (for fedramp accounts)
// X-OpenAI-Fedramp. It does NOT touch Authorization or the request body.
func ApplyFaithfulCodexIdentityHeaders(req *http.Request, accountID string, isFedramp bool) {
	if req == nil {
		return
	}
	req.Header.Set("Originator", codexUsageOriginator)
	req.Header.Set("User-Agent", codexUsageUserAgent)
	if accountID = strings.TrimSpace(accountID); accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	if isFedramp {
		req.Header.Set("X-OpenAI-Fedramp", "true")
	}
}

// FetchCodexUsage performs a read-only GET against the Codex usage endpoint using
// the same minimal header set the Codex CLI sends. It returns the upstream status
// code and raw response body verbatim so callers can pass the JSON through.
//
// It intentionally does NOT apply any of the execution-path request shaping
// (identity-confuse rewrites, image_generation tool injection, extra session/turn
// headers). Only the headers below are sent.
func FetchCodexUsage(ctx context.Context, cfg *config.Config, proxyURL, accessToken, accountID string, isFedramp bool) (int, []byte, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return 0, nil, fmt.Errorf("codex usage: access token is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	endpoint := strings.TrimSpace(os.Getenv(codexUsageURLEnv))
	if endpoint == "" {
		endpoint = codexUsageURL
	}

	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL

	httpClient := util.SetProxy(&sdkCfg, &http.Client{Timeout: codexUsageTimeout})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("codex usage: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID = strings.TrimSpace(accountID); accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	req.Header.Set("Originator", codexUsageOriginator)
	req.Header.Set("User-Agent", codexUsageUserAgent)
	req.Header.Set("Accept", "application/json")
	if isFedramp {
		req.Header.Set("X-OpenAI-Fedramp", "true")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("codex usage: request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("codex usage: read response: %w", err)
	}
	return resp.StatusCode, body, nil
}
