// Package management provides the management API handlers. This file implements
// a read-only Codex usage/quota lookup that mirrors the Codex CLI request and
// never touches the execution (/responses) endpoint.
package management

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	codex "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const codexUsageTokenRefreshLeeway = 30 * time.Second

// GetCodexUsage reads Codex usage/quota for a specific credential.
//
// Endpoint:
//
//	GET /v0/management/codex-usage?auth_index=<index>
//
// It sends the same read-only request the Codex CLI uses and returns the upstream
// status code and JSON body verbatim. It does not call any execution endpoint.
func (h *Handler) GetCodexUsage(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("authIndex"))
	}
	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found for auth_index"})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth is not a codex credential"})
		return
	}

	accessToken, errToken := h.ensureCodexAccessToken(c, auth)
	if errToken != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errToken.Error()})
		return
	}
	if accessToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "codex access token not found"})
		return
	}

	accountID := codexUsageMetaString(auth.Metadata, "account_id")
	if accountID == "" {
		accountID = codexUsageMetaString(auth.Metadata, "chatgpt_account_id")
	}
	isFedramp, _ := auth.Metadata["chatgpt_account_is_fedramp"].(bool)

	status, body, errFetch := codex.FetchCodexUsage(c.Request.Context(), h.cfg, auth.ProxyURL, accessToken, accountID, isFedramp)
	if errFetch != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errFetch.Error()})
		return
	}

	var parsed any
	if json.Unmarshal(body, &parsed) == nil {
		c.JSON(http.StatusOK, gin.H{"status_code": status, "body": parsed})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status_code": status, "body": string(body)})
}

// ensureCodexAccessToken returns a usable access token for the credential,
// refreshing it when expired and a refresh token is present, and persisting the
// refreshed token through the auth manager.
func (h *Handler) ensureCodexAccessToken(c *gin.Context, auth *coreauth.Auth) (string, error) {
	accessToken := codexUsageMetaString(auth.Metadata, "access_token")
	if accessToken != "" {
		if expiresAt, ok := auth.ExpirationTime(); !ok || time.Now().Add(codexUsageTokenRefreshLeeway).Before(expiresAt) {
			return accessToken, nil
		}
	}

	refreshToken := codexUsageMetaString(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return accessToken, nil
	}

	svc := codex.NewCodexAuthWithProxyURL(h.cfg, auth.ProxyURL)
	tokenData, errRefresh := svc.RefreshTokensWithRetry(c.Request.Context(), refreshToken, 3)
	if errRefresh != nil {
		if accessToken != "" {
			return accessToken, nil
		}
		return "", errRefresh
	}
	if strings.TrimSpace(tokenData.AccessToken) == "" {
		return accessToken, nil
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

	if _, errUpdate := h.authManager.Update(c.Request.Context(), auth); errUpdate != nil {
		log.Warnf("codex usage: persist refreshed token failed: %v", errUpdate)
	}
	return tokenData.AccessToken, nil
}

func codexUsageMetaString(m map[string]any, key string) string {
	if len(m) == 0 {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
