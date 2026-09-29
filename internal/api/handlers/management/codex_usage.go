// Package management provides the management API handlers. This file implements
// a read-only Codex usage/quota lookup that mirrors the Codex CLI request and
// never touches the execution (/responses) endpoint.
package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	codex "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const codexUsageTokenRefreshLeeway = 30 * time.Second

// GetCodexUsage reads Codex usage/quota for one credential or, when no identifier
// is provided, for every faithful-mode Codex credential.
//
// Endpoint:
//
//	GET /v0/management/codex-usage?auth_index=<index>
//	GET /v0/management/codex-usage?email=<email>
//	GET /v0/management/codex-usage?account_id=<account_id>
//	GET /v0/management/codex-usage            (batch: all codex_faithful configs)
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
	email := strings.TrimSpace(c.Query("email"))
	accountID := strings.TrimSpace(c.Query("account_id"))
	if accountID == "" {
		accountID = strings.TrimSpace(c.Query("accountId"))
	}

	// Batch mode: no identifier -> query every faithful-mode codex credential.
	if authIndex == "" && email == "" && accountID == "" {
		h.getCodexUsageBatch(c)
		return
	}

	auth, errResolve := h.findCodexAuthByIdentifier(authIndex, email, accountID)
	if errResolve != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errResolve.Error()})
		return
	}
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "codex auth not found for the provided identifier"})
		return
	}

	status, parsed, errFetch := h.fetchCodexUsageForAuth(c, auth)
	if errFetch != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errFetch.Error()})
		return
	}

	auth.EnsureIndex()
	c.JSON(http.StatusOK, gin.H{
		"status_code": status,
		"body":        parsed,
		"auth_index":  auth.Index,
		"email":       codexUsageMetaString(auth.Metadata, "email"),
	})
}

// getCodexUsageBatch queries usage for every faithful-mode codex credential and
// returns a per-account result list. One account failing does not abort the rest.
func (h *Handler) getCodexUsageBatch(c *gin.Context) {
	results := make([]gin.H, 0)
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		if !codexFaithfulEnabled(auth.Metadata) {
			continue
		}
		auth.EnsureIndex()
		entry := gin.H{
			"auth_index": auth.Index,
			"email":      codexUsageMetaString(auth.Metadata, "email"),
		}
		status, parsed, errFetch := h.fetchCodexUsageForAuth(c, auth)
		if errFetch != nil {
			entry["error"] = errFetch.Error()
		} else {
			entry["status_code"] = status
			entry["body"] = parsed
		}
		results = append(results, entry)
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// fetchCodexUsageForAuth resolves the token and performs the read-only usage
// lookup for a single codex auth, returning the upstream status and parsed body.
func (h *Handler) fetchCodexUsageForAuth(c *gin.Context, auth *coreauth.Auth) (int, any, error) {
	accessToken, errToken := h.ensureCodexAccessToken(c, auth)
	if errToken != nil {
		return 0, nil, errToken
	}
	if accessToken == "" {
		return 0, nil, fmt.Errorf("codex access token not found")
	}

	accountID := codexUsageMetaString(auth.Metadata, "account_id")
	if accountID == "" {
		accountID = codexUsageMetaString(auth.Metadata, "chatgpt_account_id")
	}
	isFedramp, _ := auth.Metadata["chatgpt_account_is_fedramp"].(bool)

	status, body, errFetch := codex.FetchCodexUsage(c.Request.Context(), h.cfg, auth.ProxyURL, accessToken, accountID, isFedramp)
	if errFetch != nil {
		return 0, nil, errFetch
	}

	var parsed any
	if json.Unmarshal(body, &parsed) == nil {
		return status, parsed, nil
	}
	return status, string(body), nil
}

// findCodexAuthByIdentifier locates a unique codex credential by auth index,
// email, or account id. It returns an error when the identifier is ambiguous.
func (h *Handler) findCodexAuthByIdentifier(authIndex, email, accountID string) (*coreauth.Auth, error) {
	var matches []*coreauth.Auth
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		auth.EnsureIndex()
		if authIndex != "" {
			if auth.Index == authIndex {
				return auth, nil
			}
			continue
		}
		if email != "" && !strings.EqualFold(codexUsageMetaString(auth.Metadata, "email"), email) {
			continue
		}
		if accountID != "" {
			metaAccount := codexUsageMetaString(auth.Metadata, "account_id")
			metaChatgpt := codexUsageMetaString(auth.Metadata, "chatgpt_account_id")
			if !strings.EqualFold(metaAccount, accountID) && !strings.EqualFold(metaChatgpt, accountID) {
				continue
			}
		}
		matches = append(matches, auth)
	}
	if authIndex != "" {
		return nil, nil
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("identifier matches %d codex configs; specify auth_index", len(matches))
	}
}

// codexFaithfulEnabled reports whether the credential metadata opts into faithful
// passthrough. Mirrors the executor flag without importing that package.
func codexFaithfulEnabled(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	for _, key := range []string{"codex_faithful", "faithful_passthrough"} {
		switch v := meta[key].(type) {
		case bool:
			if v {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(v), "true") {
				return true
			}
		}
	}
	return false
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
