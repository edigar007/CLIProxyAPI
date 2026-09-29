package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type patTestStore struct {
	baseDir string
	saved   map[string]*coreauth.Auth
}

func (s *patTestStore) SetBaseDir(dir string) { s.baseDir = dir }
func (s *patTestStore) List(context.Context) ([]*coreauth.Auth, error) {
	auths := make([]*coreauth.Auth, 0, len(s.saved))
	for _, auth := range s.saved {
		auths = append(auths, auth)
	}
	return auths, nil
}
func (s *patTestStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	if s.saved == nil {
		s.saved = make(map[string]*coreauth.Auth)
	}
	s.saved[auth.ID] = auth
	return "store://" + auth.ID, nil
}
func (s *patTestStore) Delete(context.Context, string) error { return nil }

func TestImportCodexPersonalAccessToken(t *testing.T) {
	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir}
	store := &patTestStore{}
	const token = "at-test-secret"
	load := func(_ context.Context, _ *config.Config, got string) (*codexauth.PersonalAccessTokenMetadata, error) {
		if got != token {
			t.Fatalf("token passed to whoami = %q", got)
		}
		return &codexauth.PersonalAccessTokenMetadata{
			Email:            "test@example.com",
			ChatgptAccountID: "account-123",
			ChatgptPlanType:  "team",
		}, nil
	}
	path, err := importCodexPersonalAccessToken(context.Background(), cfg, token, load, store)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "store://codex-") || store.baseDir != authDir || len(store.saved) != 1 {
		t.Fatalf("credential not saved through injected store: path=%q dir=%q count=%d", path, store.baseDir, len(store.saved))
	}
	var record *coreauth.Auth
	for _, record = range store.saved {
		break
	}
	if record.Provider != "codex" || record.ID != record.FileName {
		t.Fatalf("wrong credential identity: provider=%q id=%q filename=%q", record.Provider, record.ID, record.FileName)
	}
	if record.Metadata["type"] != "codex" || record.Metadata["access_token"] != token || record.Metadata["account_id"] != "account-123" {
		t.Fatalf("unexpected credential fields: type=%v account_id=%v has_access_token=%v", record.Metadata["type"], record.Metadata["account_id"], record.Metadata["access_token"] == token)
	}
	if record.Metadata["codex_faithful"] != true || record.Metadata["websockets"] != true {
		t.Fatalf("new PAT must enable faithful websocket mode: faithful=%v websockets=%v", record.Metadata["codex_faithful"], record.Metadata["websockets"])
	}
	if _, ok := record.Metadata["personal_access_token"]; ok {
		t.Fatal("legacy personal_access_token field must not be written")
	}
	if _, err := os.Stat(authDir); err != nil {
		t.Fatalf("test directory disappeared: %v", err)
	}
	if entries, err := os.ReadDir(authDir); err != nil || len(entries) != 0 {
		t.Fatalf("non-file store caused local file writes: entries=%d err=%v", len(entries), err)
	}
}

func TestImportCodexPersonalAccessTokenRejectsInvalidOrFailedValidation(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	store := &patTestStore{}
	called := false
	load := func(_ context.Context, _ *config.Config, _ string) (*codexauth.PersonalAccessTokenMetadata, error) {
		called = true
		return nil, errors.New("validation failed")
	}
	if _, err := importCodexPersonalAccessToken(context.Background(), cfg, "invalid-secret", load, store); err == nil || strings.Contains(err.Error(), "invalid-secret") {
		t.Fatalf("invalid token error = %v", err)
	}
	if called {
		t.Fatal("invalid token reached validation endpoint")
	}
	if _, err := importCodexPersonalAccessToken(context.Background(), cfg, "at-secret", load, store); err == nil || strings.Contains(err.Error(), "at-secret") {
		t.Fatalf("validation error = %v", err)
	}
	if len(store.saved) != 0 {
		t.Fatalf("failed validation wrote %d credentials", len(store.saved))
	}
}

func TestImportCodexPersonalAccessTokenRotatesSameAccount(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	store := &patTestStore{}
	load := func(_ context.Context, _ *config.Config, _ string) (*codexauth.PersonalAccessTokenMetadata, error) {
		return &codexauth.PersonalAccessTokenMetadata{
			Email:            "test@example.com",
			ChatgptAccountID: "account-123",
			ChatgptPlanType:  "team",
		}, nil
	}
	_, err := importCodexPersonalAccessToken(context.Background(), cfg, "at-old-token", load, store)
	if err != nil {
		t.Fatal(err)
	}
	var first *coreauth.Auth
	for _, first = range store.saved {
		break
	}
	first.Metadata["priority"] = float64(1)
	first.Metadata["note"] = "operator note"
	first.Metadata["prefix"] = "teamA"
	first.Metadata["proxy_url"] = "https://proxy.example.test"
	first.Metadata["weight"] = float64(2)
	first.Metadata["excluded_models"] = []any{"blocked-model"}
	first.Metadata["codex_faithful"] = false
	first.Metadata["websockets"] = false
	first.Metadata["personal_access_token"] = "at-legacy-secret"
	first.Metadata["refresh_token"] = "old-refresh-secret"
	first.Metadata["disabled"] = true

	_, err = importCodexPersonalAccessToken(context.Background(), cfg, "at-new-token", load, store)
	if err != nil {
		t.Fatalf("same-account rotation: %v", err)
	}
	got := store.saved[first.ID].Metadata
	if got["access_token"] != "at-new-token" || got["account_id"] != "account-123" {
		t.Fatalf("rotation failed to replace token for same account: replaced=%v account=%v", got["access_token"] == "at-new-token", got["account_id"])
	}
	for key, want := range map[string]any{
		"priority": float64(1), "note": "operator note", "prefix": "teamA",
		"proxy_url": "https://proxy.example.test", "weight": float64(2),
		"codex_faithful": false, "websockets": false, "disabled": true,
	} {
		if got[key] != want {
			t.Errorf("operator setting %s not preserved: got=%v want=%v", key, got[key], want)
		}
	}
	if _, ok := got["excluded_models"]; !ok {
		t.Error("model exclusions not preserved")
	}
	if _, ok := got["personal_access_token"]; ok {
		t.Error("legacy secret preserved")
	}
	if _, ok := got["refresh_token"]; ok {
		t.Error("obsolete refresh token preserved")
	}
}

func TestImportCodexPersonalAccessTokenRejectsConflictingAccount(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	store := &patTestStore{}
	load := func(_ context.Context, _ *config.Config, _ string) (*codexauth.PersonalAccessTokenMetadata, error) {
		return &codexauth.PersonalAccessTokenMetadata{
			Email:            "test@example.com",
			ChatgptAccountID: "account-123",
			ChatgptPlanType:  "team",
		}, nil
	}
	_, err := importCodexPersonalAccessToken(context.Background(), cfg, "at-old-token", load, store)
	if err != nil {
		t.Fatal(err)
	}
	var previous *coreauth.Auth
	for _, previous = range store.saved {
		break
	}
	previous.Metadata["account_id"] = "another-account"
	_, err = importCodexPersonalAccessToken(context.Background(), cfg, "at-new-token", load, store)
	if err == nil {
		t.Fatal("must not replace a conflicting account")
	}
	if store.saved[previous.ID].Metadata["access_token"] != "at-old-token" {
		t.Fatal("conflicting credential changed")
	}
}

func TestImportCodexPersonalAccessTokenFileStoreRotation(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	store := sdkAuth.NewFileTokenStore()
	load := func(_ context.Context, _ *config.Config, _ string) (*codexauth.PersonalAccessTokenMetadata, error) {
		return &codexauth.PersonalAccessTokenMetadata{
			Email:            "Test.Name@example.com",
			ChatgptAccountID: "account-123",
			ChatgptPlanType:  "team",
		}, nil
	}
	path, err := importCodexPersonalAccessToken(context.Background(), cfg, "at-old-token", load, store)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var existing map[string]any
	if err = json.Unmarshal(raw, &existing); err != nil {
		t.Fatal(err)
	}
	existing["note"] = "retain this note"
	existing["codex_faithful"] = false
	raw, err = json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rotated, err := importCodexPersonalAccessToken(context.Background(), cfg, "at-new-token", load, store)
	if err != nil || rotated != path {
		t.Fatalf("file-store rotation path=%q err=%v", rotated, err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["access_token"] != "at-new-token" || got["note"] != "retain this note" || got["codex_faithful"] != false {
		t.Fatalf("rotated file did not preserve operator settings: token_replaced=%v note=%v faithful=%v",
			got["access_token"] == "at-new-token", got["note"], got["codex_faithful"])
	}
}

func TestImportCodexPersonalAccessTokenDoesNotOverwriteUnlistedFile(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	store := sdkAuth.NewFileTokenStore()
	load := func(_ context.Context, _ *config.Config, _ string) (*codexauth.PersonalAccessTokenMetadata, error) {
		return &codexauth.PersonalAccessTokenMetadata{
			Email:            "test@example.com",
			ChatgptAccountID: "account-123",
			ChatgptPlanType:  "team",
		}, nil
	}
	path, err := importCodexPersonalAccessToken(context.Background(), cfg, "at-old-token", load, store)
	if err != nil {
		t.Fatal(err)
	}
	const corrupted = "not-json"
	if err = os.WriteFile(path, []byte(corrupted), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = importCodexPersonalAccessToken(context.Background(), cfg, "at-new-token", load, store); err == nil {
		t.Fatal("must not overwrite an unreadable credential")
	}
	raw, err := os.ReadFile(filepath.Join(cfg.AuthDir, filepath.Base(path)))
	if err != nil || string(raw) != corrupted {
		t.Fatalf("unlisted credential changed: err=%v", err)
	}
}

func TestImportCodexPersonalAccessTokenCreatesMissingFileStoreDir(t *testing.T) {
	cfg := &config.Config{AuthDir: filepath.Join(t.TempDir(), "new-auths")}
	store := sdkAuth.NewFileTokenStore()
	load := func(_ context.Context, _ *config.Config, _ string) (*codexauth.PersonalAccessTokenMetadata, error) {
		return &codexauth.PersonalAccessTokenMetadata{
			Email:            "test@example.com",
			ChatgptAccountID: "account-123",
			ChatgptPlanType:  "team",
		}, nil
	}
	path, err := importCodexPersonalAccessToken(context.Background(), cfg, "at-new-token", load, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("new credential not persisted: %v", err)
	}
}

func TestImportCodexPersonalAccessTokenRotatesMixedCaseMirrorID(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	store := &patTestStore{}
	load := func(_ context.Context, _ *config.Config, _ string) (*codexauth.PersonalAccessTokenMetadata, error) {
		return &codexauth.PersonalAccessTokenMetadata{
			Email:            "Test.Name@example.com",
			ChatgptAccountID: "account-123",
			ChatgptPlanType:  "team",
		}, nil
	}
	_, err := importCodexPersonalAccessToken(context.Background(), cfg, "at-old-token", load, store)
	if err != nil {
		t.Fatal(err)
	}
	var original *coreauth.Auth
	for _, original = range store.saved {
		break
	}
	delete(store.saved, original.ID)
	original.ID = original.FileName // Mirror stores preserve filename case on Windows.
	store.saved[original.ID] = original
	if _, err = importCodexPersonalAccessToken(context.Background(), cfg, "at-new-token", load, store); err != nil {
		t.Fatalf("mixed-case mirror rotation: %v", err)
	}
	if store.saved[original.ID].Metadata["access_token"] != "at-new-token" {
		t.Fatal("mirror credential was not rotated")
	}
}
