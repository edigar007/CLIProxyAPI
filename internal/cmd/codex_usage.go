// Package cmd contains CLI helpers. This file implements a read-only Codex usage
// lookup that mirrors the Codex CLI's own usage query. It never calls the
// execution (/responses) endpoint.
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const codexUsageTokenRefreshLeeway = 30 * time.Second

// DoCodexUsage reads the Codex usage/quota for the first enabled Codex credential
// and prints the raw upstream JSON. It refreshes an expired access token when a
// refresh token is available, but performs no execution requests.
func DoCodexUsage(cfg *config.Config) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if resolved, errResolve := util.ResolveAuthDir(cfg.AuthDir); errResolve == nil {
		cfg.AuthDir = resolved
	}

	authDir := strings.TrimSpace(cfg.AuthDir)
	if authDir == "" {
		authDir = "auths"
	}

	fileStore := sdkauth.NewFileTokenStore()
	fileStore.SetBaseDir(authDir)

	ctx := context.Background()
	auths, errList := fileStore.List(ctx)
	if errList != nil {
		fmt.Fprintf(os.Stderr, "codex-usage: failed to list auth files: %v\n", errList)
		os.Exit(1)
	}

	auth := findEnabledCodexAuth(auths)
	if auth == nil {
		fmt.Fprintf(os.Stderr, "codex-usage: no enabled codex auth found in %s\n", authDir)
		os.Exit(1)
	}

	fmt.Printf("Using auth: id=%s label=%s\n", auth.ID, auth.Label)

	accessToken, errToken := ensureCodexUsageAccessToken(ctx, fileStore, cfg, auth)
	if errToken != nil {
		fmt.Fprintf(os.Stderr, "codex-usage: failed to prepare access token: %v\n", errToken)
		os.Exit(1)
	}

	accountID := codexUsageMetaString(auth.Metadata, "account_id")
	if accountID == "" {
		accountID = codexUsageMetaString(auth.Metadata, "chatgpt_account_id")
	}
	isFedramp, _ := auth.Metadata["chatgpt_account_is_fedramp"].(bool)

	status, body, errFetch := codexauth.FetchCodexUsage(ctx, cfg, auth.ProxyURL, accessToken, accountID, isFedramp)
	if errFetch != nil {
		fmt.Fprintf(os.Stderr, "codex-usage: request failed: %v\n", errFetch)
		os.Exit(1)
	}

	fmt.Printf("HTTP %d\n", status)
	if pretty, errPretty := prettyCodexUsageJSON(body); errPretty == nil {
		fmt.Println(string(pretty))
	} else {
		fmt.Println(string(body))
	}
	if status < 200 || status >= 300 {
		os.Exit(1)
	}
}

func findEnabledCodexAuth(auths []*coreauth.Auth) *coreauth.Auth {
	for _, auth := range auths {
		if auth == nil || auth.Disabled {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		if codexUsageMetaString(auth.Metadata, "access_token") == "" && codexUsageMetaString(auth.Metadata, "refresh_token") == "" {
			continue
		}
		return auth
	}
	return nil
}

// ensureCodexUsageAccessToken returns a usable access token, refreshing it when
// expired and a refresh token is present, and persisting any refreshed token.
func ensureCodexUsageAccessToken(ctx context.Context, store *sdkauth.FileTokenStore, cfg *config.Config, auth *coreauth.Auth) (string, error) {
	accessToken := codexUsageMetaString(auth.Metadata, "access_token")
	if accessToken != "" {
		if expiresAt, ok := auth.ExpirationTime(); !ok || time.Now().Add(codexUsageTokenRefreshLeeway).Before(expiresAt) {
			return accessToken, nil
		}
	}

	refreshToken := codexUsageMetaString(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		if accessToken != "" {
			return accessToken, nil
		}
		return "", fmt.Errorf("missing access_token and refresh_token")
	}

	svc := codexauth.NewCodexAuthWithProxyURL(cfg, auth.ProxyURL)
	tokenData, errRefresh := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if errRefresh != nil {
		return "", errRefresh
	}
	if strings.TrimSpace(tokenData.AccessToken) == "" {
		return "", fmt.Errorf("refresh response did not include access_token")
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = tokenData.IDToken
	auth.Metadata["access_token"] = tokenData.AccessToken
	if tokenData.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenData.RefreshToken
	}
	if tokenData.AccountID != "" {
		auth.Metadata["account_id"] = tokenData.AccountID
	}
	if tokenData.Email != "" {
		auth.Metadata["email"] = tokenData.Email
	}
	auth.Metadata["expired"] = tokenData.Expire
	auth.Metadata["type"] = "codex"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)

	if _, errSave := store.Save(ctx, auth); errSave != nil {
		return "", fmt.Errorf("failed to save refreshed auth: %w", errSave)
	}
	return tokenData.AccessToken, nil
}

func codexUsageMetaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func prettyCodexUsageJSON(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
