// Package cmd contains CLI helpers. This file implements importing a Codex
// personal access token (at-...) into the auth store without requiring a browser
// login or the management UI.
package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// DoCodexPersonalAccessTokenLogin imports an official Codex personal access
// token (prefix "at-") into the configured auth directory.
//
// token may be the literal "at-..." value or empty/"-", in which case the token
// is read from stdin (pipe-friendly: printf '%s' $TOKEN | ./server -codex-pat -).
//
// The function calls the OpenAI whoami endpoint to validate the token and
// retrieve workspace metadata before persisting the auth file.
func DoCodexPersonalAccessTokenLogin(cfg *config.Config, token string) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if resolved, errResolve := util.ResolveAuthDir(cfg.AuthDir); errResolve == nil {
		cfg.AuthDir = resolved
	}

	// Read token from stdin when not provided or set to "-".
	token = strings.TrimSpace(token)
	if token == "" || token == "-" {
		fmt.Print("Enter Codex personal access token (at-...): ")
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			token = strings.TrimSpace(scanner.Text())
		}
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "codex-pat: token is empty")
		os.Exit(1)
	}
	if !codexauth.IsPersonalAccessToken(token) {
		fmt.Fprintf(os.Stderr, "codex-pat: token must start with \"at-\", got %q\n", token[:min(len(token), 12)])
		os.Exit(1)
	}

	fmt.Printf("Validating token (prefix: %s...)...\n", token[:min(len(token), 12)])

	proxyURL := strings.TrimSpace(cfg.ProxyURL)
	meta, errWhoAmI := codexauth.LoadPersonalAccessTokenMetadata(context.Background(), cfg, proxyURL, token)
	if errWhoAmI != nil {
		fmt.Fprintf(os.Stderr, "codex-pat: whoami failed: %v\n", errWhoAmI)
		os.Exit(1)
	}

	planType := strings.ToLower(strings.TrimSpace(meta.ChatgptPlanType))
	email := strings.TrimSpace(meta.Email)
	accountID := strings.TrimSpace(meta.ChatgptAccountID)

	fmt.Printf("  email:      %s\n", email)
	fmt.Printf("  account_id: %s\n", accountID)
	fmt.Printf("  plan_type:  %s\n", planType)

	// Build the file name using the same convention as the OAuth login flow.
	hashAccountID := ""
	if accountID != "" {
		digest := sha256.Sum256([]byte(accountID))
		hashAccountID = hex.EncodeToString(digest[:])[:8]
	}
	fileName := codexauth.CredentialFileName(email, planType, hashAccountID, true)

	authDir := strings.TrimSpace(cfg.AuthDir)
	if authDir == "" {
		authDir = "auths"
	}
	if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
		fmt.Fprintf(os.Stderr, "codex-pat: create auth dir: %v\n", errMkdir)
		os.Exit(1)
	}

	record := map[string]any{
		"type":                       "codex",
		"email":                      email,
		"account_id":                 accountID,
		"chatgpt_account_id":         accountID,
		"chatgpt_user_id":            strings.TrimSpace(meta.ChatgptUserID),
		"plan_type":                  planType,
		"chatgpt_plan_type":          planType,
		"chatgpt_account_is_fedramp": meta.ChatgptAccountIsFedramp,
		"personal_access_token":      token,
		"disabled":                   false,
	}

	raw, errMarshal := json.MarshalIndent(record, "", "  ")
	if errMarshal != nil {
		fmt.Fprintf(os.Stderr, "codex-pat: marshal json: %v\n", errMarshal)
		os.Exit(1)
	}

	dstPath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(dstPath, raw, 0o600); errWrite != nil {
		fmt.Fprintf(os.Stderr, "codex-pat: write file: %v\n", errWrite)
		os.Exit(1)
	}

	fmt.Printf("\nSaved to: %s\n", dstPath)
	fmt.Println("Codex personal access token imported successfully!")
	fmt.Println("Restart the server (or let it hot-reload) to pick up the new credential.")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
