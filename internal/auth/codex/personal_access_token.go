package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

const personalAccessTokenWhoAmIURL = "https://auth.openai.com/api/accounts/v1/user-auth-credential/whoami"

// PersonalAccessTokenMetadata contains the account details used to identify a Codex PAT.
type PersonalAccessTokenMetadata struct {
	Email                   string `json:"email"`
	ChatgptUserID           string `json:"chatgpt_user_id"`
	ChatgptAccountID        string `json:"chatgpt_account_id"`
	ChatgptPlanType         string `json:"chatgpt_plan_type"`
	ChatgptAccountIsFedramp bool   `json:"chatgpt_account_is_fedramp"`
}

// NormalizePersonalAccessToken validates and trims a Codex personal access token.
func NormalizePersonalAccessToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "at-") || len(token) <= len("at-") {
		return "", fmt.Errorf("Codex PAT must start with at- and must not be empty")
	}
	return token, nil
}

// LoadPersonalAccessTokenMetadata validates a Codex PAT and retrieves its account details.
func LoadPersonalAccessTokenMetadata(ctx context.Context, cfg *config.Config, token string) (*PersonalAccessTokenMetadata, error) {
	token, err := NormalizePersonalAccessToken(token)
	if err != nil {
		return nil, err
	}
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if strings.TrimSpace(cfg.ProxyURL) != "" {
			sdkCfg.ProxyURL = cfg.ProxyURL
		}
	}
	return loadPersonalAccessTokenMetadata(ctx, util.SetProxy(&sdkCfg, &http.Client{}), personalAccessTokenWhoAmIURL, token)
}

func loadPersonalAccessTokenMetadata(ctx context.Context, client *http.Client, endpoint, token string) (*PersonalAccessTokenMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create Codex PAT validation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("validate Codex PAT: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("close Codex PAT validation response")
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Upstream error bodies can contain sensitive request data.
		return nil, fmt.Errorf("validate Codex PAT: HTTP %d", resp.StatusCode)
	}
	var metadata PersonalAccessTokenMetadata
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("decode Codex PAT account metadata: %w", err)
	}
	metadata.Email = strings.TrimSpace(metadata.Email)
	metadata.ChatgptUserID = strings.TrimSpace(metadata.ChatgptUserID)
	metadata.ChatgptAccountID = strings.TrimSpace(metadata.ChatgptAccountID)
	metadata.ChatgptPlanType = strings.TrimSpace(metadata.ChatgptPlanType)
	if metadata.ChatgptAccountID == "" {
		return nil, fmt.Errorf("Codex PAT account metadata missing account ID")
	}
	return &metadata, nil
}
