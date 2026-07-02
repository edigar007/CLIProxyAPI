package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const (
	personalAccessTokenPrefix         = "at-"
	personalAccessTokenAuthAPIBaseURL = "https://auth.openai.com/api/accounts"
	personalAccessTokenAuthAPIBaseEnv = "CODEX_AUTHAPI_BASE_URL"
	personalAccessTokenWhoAmIPath     = "/v1/user-auth-credential/whoami"
)

// PersonalAccessTokenMetadata contains the workspace-scoped metadata returned
// by the official Codex personal-access-token whoami endpoint.
type PersonalAccessTokenMetadata struct {
	Email                   string `json:"email"`
	ChatgptUserID           string `json:"chatgpt_user_id"`
	ChatgptAccountID        string `json:"chatgpt_account_id"`
	ChatgptPlanType         string `json:"chatgpt_plan_type"`
	ChatgptAccountIsFedramp bool   `json:"chatgpt_account_is_fedramp"`
}

// IsPersonalAccessToken reports whether the provided value matches the official
// enterprise personal access token prefix used by Codex.
func IsPersonalAccessToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), personalAccessTokenPrefix)
}

// LoadPersonalAccessTokenMetadata resolves account metadata for an official
// Codex personal access token using the same whoami endpoint the upstream client uses.
func LoadPersonalAccessTokenMetadata(ctx context.Context, cfg *config.Config, proxyURL string, accessToken string) (*PersonalAccessTokenMetadata, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("codex personal access token metadata: token is empty")
	}
	if !IsPersonalAccessToken(accessToken) {
		return nil, fmt.Errorf("codex personal access token metadata: token does not use at- prefix")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	authAPIBaseURL := strings.TrimSpace(os.Getenv(personalAccessTokenAuthAPIBaseEnv))
	if authAPIBaseURL == "" {
		authAPIBaseURL = personalAccessTokenAuthAPIBaseURL
	}
	endpoint := strings.TrimRight(authAPIBaseURL, "/") + personalAccessTokenWhoAmIPath

	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL

	httpClient := util.SetProxy(&sdkCfg, &http.Client{})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("codex personal access token metadata: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex personal access token metadata: request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codex personal access token metadata: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("codex personal access token metadata: whoami status %d: %s", resp.StatusCode, message)
	}

	var metadata PersonalAccessTokenMetadata
	if err = json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("codex personal access token metadata: decode response: %w", err)
	}
	if strings.TrimSpace(metadata.ChatgptAccountID) == "" {
		return nil, fmt.Errorf("codex personal access token metadata: missing chatgpt_account_id")
	}

	metadata.Email = strings.TrimSpace(metadata.Email)
	metadata.ChatgptUserID = strings.TrimSpace(metadata.ChatgptUserID)
	metadata.ChatgptAccountID = strings.TrimSpace(metadata.ChatgptAccountID)
	metadata.ChatgptPlanType = strings.TrimSpace(metadata.ChatgptPlanType)
	return &metadata, nil
}
