package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// DoCodexPersonalAccessTokenLogin imports a Codex PAT from an argument or stdin.
func DoCodexPersonalAccessTokenLogin(cfg *config.Config, token string) error {
	token = strings.TrimSpace(token)
	if token == "-" {
		if info, errStat := os.Stdin.Stat(); errStat != nil {
			return fmt.Errorf("inspect Codex PAT stdin: %w", errStat)
		} else if info.Mode()&os.ModeCharDevice != 0 {
			return fmt.Errorf("pipe the Codex PAT to stdin instead of typing it into an echoing terminal")
		}
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read Codex PAT from stdin: %w", err)
			}
			return fmt.Errorf("Codex PAT is empty")
		}
		token = strings.TrimSpace(scanner.Text())
	}
	_, err := importCodexPersonalAccessToken(context.Background(), cfg, token, codexauth.LoadPersonalAccessTokenMetadata, sdkAuth.GetTokenStore())
	if err != nil {
		return err
	}
	fmt.Println("Codex personal access token imported successfully")
	return nil
}

func importCodexPersonalAccessToken(
	ctx context.Context,
	cfg *config.Config,
	token string,
	load func(context.Context, *config.Config, string) (*codexauth.PersonalAccessTokenMetadata, error),
	store coreauth.Store,
) (string, error) {
	var err error
	token, err = codexauth.NormalizePersonalAccessToken(token)
	if err != nil {
		return "", err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	meta, err := load(ctx, cfg, token)
	if err != nil {
		return "", fmt.Errorf("validate Codex PAT: %w", err)
	}
	if meta == nil || strings.TrimSpace(meta.ChatgptAccountID) == "" {
		return "", fmt.Errorf("Codex PAT account metadata missing account ID")
	}

	email := strings.TrimSpace(meta.Email)
	if strings.IndexFunc(email, func(r rune) bool {
		return r < 32 || strings.ContainsRune("/\\:<>\"|?*", r)
	}) >= 0 {
		return "", fmt.Errorf("Codex PAT account email cannot be used as a file name")
	}
	accountID := strings.TrimSpace(meta.ChatgptAccountID)
	planType := strings.ToLower(strings.TrimSpace(meta.ChatgptPlanType))
	digest := sha256.Sum256([]byte(accountID))
	fileName := codexauth.CredentialFileName(email, planType, hex.EncodeToString(digest[:])[:8], true)
	recordID := fileName
	if runtime.GOOS == "windows" && isFileTokenStore(store) {
		recordID = strings.ToLower(fileName)
	}
	authDir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err != nil {
		return "", fmt.Errorf("resolve Codex auth directory: %w", err)
	}
	if store == nil {
		return "", fmt.Errorf("Codex PAT token store is unavailable")
	}
	if setter, ok := store.(interface{ SetBaseDir(string) }); ok {
		setter.SetBaseDir(authDir)
	}
	record := &coreauth.Auth{
		ID:       recordID,
		Provider: "codex",
		FileName: fileName,
		Metadata: map[string]any{
			"type":                       "codex",
			"email":                      email,
			"account_id":                 accountID,
			"chatgpt_account_id":         accountID,
			"chatgpt_user_id":            strings.TrimSpace(meta.ChatgptUserID),
			"plan_type":                  planType,
			"chatgpt_plan_type":          planType,
			"chatgpt_account_is_fedramp": meta.ChatgptAccountIsFedramp,
			"access_token":               token,
			"codex_faithful":             true,
			"websockets":                 true,
			"disabled":                   false,
		},
	}
	existing, err := store.List(ctx)
	if err != nil && (!errors.Is(err, os.ErrNotExist) || !isFileTokenStore(store)) {
		return "", fmt.Errorf("list Codex credentials before import: %w", err)
	}
	matched := false
	for _, candidate := range existing {
		sameFileName := candidate != nil && candidate.FileName == fileName
		if runtime.GOOS == "windows" && candidate != nil {
			sameFileName = strings.EqualFold(candidate.FileName, fileName)
		}
		if candidate != nil && (candidate.ID == recordID || sameFileName) {
			if matched || candidate.ID != recordID || !strings.EqualFold(candidate.Provider, "codex") || !sameCodexAccount(candidate.Metadata, accountID) {
				return "", fmt.Errorf("Codex PAT credential conflicts with an existing account")
			}
			matched = true
			// Keep operator settings, but never carry over obsolete secrets.
			existingMetadata := make(map[string]any, len(candidate.Metadata))
			for key, value := range candidate.Metadata {
				if key != "personal_access_token" {
					existingMetadata[key] = value
				}
			}
			delete(record.Metadata, "codex_faithful")
			delete(record.Metadata, "websockets")
			delete(record.Metadata, "disabled")
			coreauth.MergeExistingAuthMetadata(record, existingMetadata)
			if _, ok := record.Metadata["codex_faithful"]; !ok {
				record.Metadata["codex_faithful"] = true
			}
			if _, ok := record.Metadata["websockets"]; !ok {
				record.Metadata["websockets"] = true
			}
			if disabled, ok := record.Metadata["disabled"].(bool); ok {
				record.Disabled = disabled
			} else {
				record.Disabled = candidate.Disabled
			}
		}
	}
	// File and mirrored stores can skip malformed JSON during List. Check their
	// local target as well before allowing Save to replace an existing file.
	localDir := ""
	if isFileTokenStore(store) {
		localDir = authDir
	} else if mirror, ok := store.(interface{ AuthDir() string }); ok {
		localDir = mirror.AuthDir()
	}
	if localDir != "" {
		if info, errStat := os.Lstat(filepath.Join(localDir, fileName)); errStat == nil {
			if !matched || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("Codex PAT credential already exists but cannot be verified")
			}
		} else if !errors.Is(errStat, os.ErrNotExist) {
			return "", fmt.Errorf("inspect Codex PAT credential: %w", errStat)
		} else if matched {
			return "", fmt.Errorf("Codex PAT credential disappeared before rotation")
		}
	}
	path, err := store.Save(ctx, record)
	if err != nil {
		return "", fmt.Errorf("save Codex PAT credential: %w", err)
	}
	return path, nil
}

func isFileTokenStore(store coreauth.Store) bool {
	_, ok := store.(*sdkAuth.FileTokenStore)
	return ok
}

func sameCodexAccount(metadata map[string]any, accountID string) bool {
	matched := false
	for _, key := range []string{"account_id", "chatgpt_account_id"} {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			if strings.TrimSpace(value) != accountID {
				return false
			}
			matched = true
		}
	}
	return matched
}
