package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func testOAuthJWTForCommonTest(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return header + "." + body + ".sig"
}

func TestPlanSessionStartupContinueUsesLatestNonEmptySession(t *testing.T) {
	sessionsDir := t.TempDir()
	writeTestSessionMain(t, sessionsDir, "100", `{"role":"user","content":"first"}`+"\n")
	writeTestSessionMain(t, sessionsDir, "200", `{"role":"user","content":"latest"}`+"\n")
	if err := os.Mkdir(filepath.Join(sessionsDir, "300"), 0o755); err != nil {
		t.Fatalf("mkdir empty session: %v", err)
	}

	plan, err := planSessionStartup(sessionsDir, sessionStartupOptions{ContinueLatest: true})
	if err != nil {
		t.Fatalf("planSessionStartup: %v", err)
	}
	if !plan.RestoreOnStartup {
		t.Fatal("expected restore on startup for latest session")
	}
	if got := filepath.Base(plan.SessionDir); got != "200" {
		t.Fatalf("SessionDir = %q, want %q", got, "200")
	}
}

func TestPlanSessionStartupContinueIncludesLatestEvenIfLocked(t *testing.T) {
	sessionsDir := t.TempDir()
	writeTestSessionMain(t, sessionsDir, "100", `{"role":"user","content":"first"}`+"\n")
	writeTestSessionMain(t, sessionsDir, "200", `{"role":"user","content":"locked latest"}`+"\n")
	lock, err := recovery.AcquireSessionLock(filepath.Join(sessionsDir, "200"))
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	defer lock.Release()

	plan, err := planSessionStartup(sessionsDir, sessionStartupOptions{ContinueLatest: true})
	if err != nil {
		t.Fatalf("planSessionStartup: %v", err)
	}
	if !plan.RestoreOnStartup {
		t.Fatal("expected restore on startup for latest session")
	}
	if got := filepath.Base(plan.SessionDir); got != "200" {
		t.Fatalf("SessionDir = %q, want %q", got, "200")
	}
}

func TestPlanSessionStartupContinueFallsBackToNewSession(t *testing.T) {
	sessionsDir := t.TempDir()

	plan, err := planSessionStartup(sessionsDir, sessionStartupOptions{ContinueLatest: true})
	if err != nil {
		t.Fatalf("planSessionStartup: %v", err)
	}
	if plan.RestoreOnStartup {
		t.Fatal("did not expect restore when no previous session exists")
	}
	if plan.SessionDir == "" {
		t.Fatal("expected a new session directory")
	}
	if info, err := os.Stat(plan.SessionDir); err != nil || !info.IsDir() {
		t.Fatalf("session directory was not created: %v", err)
	}
}

func TestPlanSessionStartupResumeRequiresExistingMessages(t *testing.T) {
	sessionsDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(sessionsDir, "123"), 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}

	_, err := planSessionStartup(sessionsDir, sessionStartupOptions{ResumeID: "123"})
	if err == nil {
		t.Fatal("expected error for empty resume session")
	}
}

func writeTestSessionMain(t *testing.T, sessionsDir, sessionID, content string) {
	t.Helper()
	sessionDir := filepath.Join(sessionsDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "main.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write main.jsonl: %v", err)
	}
}

func TestParseRoleModelRefAppliesDefaultVariantWhenInlineVariantMissing(t *testing.T) {
	t.Parallel()
	baseRef, variant := parseRoleModelRef("openai/gpt-5", "high")
	if baseRef != "openai/gpt-5" {
		t.Fatalf("baseRef = %q, want openai/gpt-5", baseRef)
	}
	if variant != "high" {
		t.Fatalf("variant = %q, want high", variant)
	}
}

func TestParseRoleModelRefKeepsInlineVariantOverDefault(t *testing.T) {
	t.Parallel()
	baseRef, variant := parseRoleModelRef("openai/gpt-5@low", "high")
	if baseRef != "openai/gpt-5" {
		t.Fatalf("baseRef = %q, want openai/gpt-5", baseRef)
	}
	if variant != "low" {
		t.Fatalf("variant = %q, want low", variant)
	}
}

func TestResolveModelRefStripsInlineVariant(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"test": {
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"test-model": {
					Limit: config.ModelLimit{Context: 8192, Output: 1024},
				},
			},
		},
	}

	provCfg, _, modelID, maxTokens, ctxLimit, err := resolveModelRef(
		"test/test-model@high", providers, nil, "", nil, nil,
	)
	if err != nil {
		t.Fatalf("resolveModelRef: %v", err)
	}
	if got := provCfg.Name(); got != "test" {
		t.Fatalf("provider name = %q, want test", got)
	}
	if modelID != "test-model" {
		t.Fatalf("modelID = %q, want test-model", modelID)
	}
	if maxTokens != 1024 || ctxLimit != 8192 {
		t.Fatalf("limits = (%d, %d), want (1024, 8192)", maxTokens, ctxLimit)
	}
}

func TestResolveModelRefReusesCachedProviderImpl(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"test": {
			Type:   config.ProviderTypeResponses,
			APIURL: "https://example.com/v1/responses",
			Models: map[string]config.ModelConfig{
				"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		},
	}
	cache := &providerCache{
		m:     make(map[string]*llm.ProviderConfig),
		impls: make(map[string]llm.Provider),
		auth:  config.AuthConfig{"test": {{APIKey: "k1"}}},
		cfg:   &config.Config{},
	}
	provCfg1, impl1, _, _, _, err := resolveModelRef(
		"test/test-model", providers, cache.auth, "", cache.getOrCreate, cache.getOrCreateImpl,
	)
	if err != nil {
		t.Fatalf("resolveModelRef first: %v", err)
	}
	provCfg2, impl2, _, _, _, err := resolveModelRef(
		"test/test-model", providers, cache.auth, "", cache.getOrCreate, cache.getOrCreateImpl,
	)
	if err != nil {
		t.Fatalf("resolveModelRef second: %v", err)
	}
	if provCfg1 != provCfg2 {
		t.Fatal("expected ProviderConfig to be reused")
	}
	if impl1 != impl2 {
		t.Fatal("expected Provider implementation to be reused")
	}
}

func TestTestProvidersWiresOAuthMetadataForCodexPreset(t *testing.T) {
	creds := []config.ProviderCredential{{OAuth: &config.OAuthCredential{
		Access:    testOAuthJWTForCommonTest(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acc-test"}}`),
		Refresh:   "refresh-token",
		Expires:   32503680000000,
		AccountID: "acc-test",
	}}}
	normalizedCfg := config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: config.ProviderPresetCodex,
		APIURL: config.OpenAICodexResponsesURL,
		Models: map[string]config.ModelConfig{"gpt-5.5-mini": {}},
	}
	apiKeys := config.ExtractAPIKeys(creds)
	if len(apiKeys) != 1 {
		t.Fatalf("ExtractAPIKeys len=%d, want 1", len(apiKeys))
	}
	providerConfig := llm.NewProviderConfig("codex", normalizedCfg, []string{apiKeys[0]})
	if !providerConfig.IsCodexOAuthTransport() {
		t.Fatal("expected preset codex provider to use codex transport")
	}
	tokenURL, clientID, ok, err := resolveProviderOAuthSettings(normalizedCfg, creds)
	if err != nil {
		t.Fatalf("resolveProviderOAuthSettings: %v", err)
	}
	if !ok {
		t.Fatal("expected codex preset OAuth settings to be enabled")
	}
	auth := config.AuthConfig{"codex": creds}
	var authMu sync.Mutex
	oauthMap, _ := oauthCredentialMap(creds)
	if _, found := oauthMap[apiKeys[0]]; !found {
		t.Fatal("expected oauthCredentialMap to contain OAuth access token")
	}
	providerConfig.SetOAuthRefresher(tokenURL, clientID, "", &auth, &authMu, oauthMap, "")
	if providerConfig.EffectiveProxyURL() != "" {
		t.Fatalf("unexpected effective proxy URL = %q", providerConfig.EffectiveProxyURL())
	}
}

func TestApplyInitialMCPPromptStateSyncConfiguredInjectsPrompt(t *testing.T) {
	ac := newTestAppContext(t)
	block := "## MCP (Model Context Protocol) integrations\n- **filesystem** — tools: mcp_filesystem_read\n"

	applyInitialMCPPromptState(ac, false, true, block)

	if got := ac.CtxMgr.SystemPrompt().Content; !strings.Contains(got, block) {
		t.Fatalf("system prompt missing MCP block:\n%s", got)
	}
}

func TestApplyInitialMCPPromptStateAsyncConfiguredDefersPromptInjection(t *testing.T) {
	ac := newTestAppContext(t)
	ac.MCPConfigs = []mcp.ServerConfig{{Name: "filesystem"}}
	block := "## MCP (Model Context Protocol) integrations\n- **filesystem** — tools: mcp_filesystem_read\n"

	applyInitialMCPPromptState(ac, true, true, block)

	if got := ac.CtxMgr.SystemPrompt().Content; strings.Contains(got, block) {
		t.Fatalf("system prompt unexpectedly contained MCP block:\n%s", got)
	}
}

func TestSkillLoadDirsIncludesAgentsSkillsByDefault(t *testing.T) {
	projectRoot := t.TempDir()
	chordHome := t.TempDir()
	ac := &AppContext{
		ProjectRoot: projectRoot,
		ConfigHome:  chordHome,
		Cfg:         &config.Config{},
	}

	got := skillLoadDirs(ac)
	want := []string{
		filepath.Join(projectRoot, ".chord", "skills"),
		filepath.Join(projectRoot, ".agents", "skills"),
		filepath.Join(chordHome, "skills"),
	}
	if len(got) != len(want) {
		t.Fatalf("len(skillLoadDirs) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("skillLoadDirs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSkillLoadDirsAppendsConfiguredPathsAfterDefaults(t *testing.T) {
	projectRoot := t.TempDir()
	chordHome := t.TempDir()
	globalExtra := filepath.Join(t.TempDir(), "global-extra")
	projectExtra := filepath.Join(t.TempDir(), "project-extra")
	ac := &AppContext{
		ProjectRoot: projectRoot,
		ConfigHome:  chordHome,
		Cfg: &config.Config{Skills: config.SkillsConfig{
			Paths: []string{globalExtra},
		}},
		ProjectCfg: &config.Config{Skills: config.SkillsConfig{
			Paths: []string{projectExtra},
		}},
	}

	got := skillLoadDirs(ac)
	want := []string{
		filepath.Join(projectRoot, ".chord", "skills"),
		filepath.Join(projectRoot, ".agents", "skills"),
		filepath.Join(chordHome, "skills"),
		globalExtra,
		projectExtra,
	}
	if len(got) != len(want) {
		t.Fatalf("len(skillLoadDirs) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("skillLoadDirs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWorkDirSkillChainDeepestFirst(t *testing.T) {
	projectRoot := t.TempDir()
	workDir := filepath.Join(projectRoot, "pkg", "service", "handler")
	got := WorkDirSkillChain(projectRoot, workDir)
	want := []string{
		filepath.Join(projectRoot, "pkg", "service", "handler", ".agents", "skills"),
		filepath.Join(projectRoot, "pkg", "service", ".agents", "skills"),
		filepath.Join(projectRoot, "pkg", ".agents", "skills"),
	}
	if len(got) != len(want) {
		t.Fatalf("len(WorkDirSkillChain) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("WorkDirSkillChain()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSkillLoadDirsForWorkDirInsertsChainBeforeHome(t *testing.T) {
	projectRoot := t.TempDir()
	chordHome := t.TempDir()
	workDir := filepath.Join(projectRoot, "pkg", "service")
	ac := &AppContext{
		ProjectRoot: projectRoot,
		ConfigHome:  chordHome,
		Cfg:         &config.Config{},
	}
	got := skillLoadDirsForWorkDir(ac, workDir)
	want := []string{
		filepath.Join(projectRoot, ".chord", "skills"),
		filepath.Join(projectRoot, ".agents", "skills"),
		filepath.Join(projectRoot, "pkg", "service", ".agents", "skills"),
		filepath.Join(projectRoot, "pkg", ".agents", "skills"),
		filepath.Join(chordHome, "skills"),
	}
	if len(got) != len(want) {
		t.Fatalf("len(skillLoadDirsForWorkDir) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("skillLoadDirsForWorkDir()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

type noopProvider struct{}

func (noopProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	return &message.Response{}, nil
}

func (noopProvider) Complete(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
) (*message.Response, error) {
	return &message.Response{}, nil
}

func newTestAppContext(t *testing.T) *AppContext {
	t.Helper()
	projectRoot := t.TempDir()
	sessionDir := filepath.Join(projectRoot, ".chord", "sessions", "test")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"test-model": {
				Limit: config.ModelLimit{
					Context: 8192,
					Output:  1024,
				},
			},
		},
	}, []string{"test-key"})
	ctxMgr := ctxmgr.NewManager(8192, false, 0)
	mainAgent := agent.NewMainAgent(
		context.Background(),
		llm.NewClient(providerCfg, noopProvider{}, "test-model", 1024, ""),
		ctxMgr,
		tools.NewRegistry(),
		&hook.NoopEngine{},
		sessionDir,
		"test-model",
		projectRoot,
		&config.Config{},
		nil,
	)

	return &AppContext{
		Ctx:         context.Background(),
		ProjectRoot: projectRoot,
		SessionDir:  sessionDir,
		CtxMgr:      ctxMgr,
		MainAgent:   mainAgent,
	}
}
