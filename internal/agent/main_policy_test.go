package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/tools"
)

type stubProvider struct {
	response *message.Response
	err      error
}

type sessionAwareStubProvider struct {
	stubProvider
	sessionID string
}

func (p stubProvider) CompleteStream(
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
	if p.err != nil {
		return nil, p.err
	}
	if p.response != nil {
		return p.response, nil
	}
	return &message.Response{}, nil
}

func (p stubProvider) Complete(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
) (*message.Response, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.response != nil {
		return p.response, nil
	}
	return &message.Response{}, nil
}

func (p *sessionAwareStubProvider) SetSessionID(sid string) {
	p.sessionID = sid
}

func TestStartPlanExecutionKeepsExecutionPromptAcrossRefresh(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := filepath.Join(projectRoot, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n- task\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: "primary", Description: "Builder role"},
	})
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		client := llm.NewClient(providerCfg, stubProvider{response: &message.Response{Content: "ok", StopReason: "stop"}}, "test-model", 1024, "")
		return client, "test-model", 8192, nil
	})

	a.startPlanExecution(planPath, "builder")

	assertExecutionPromptContains(t, a.ctxMgr.SystemPrompt().Content, planPath)

	a.refreshSystemPrompt()
	assertExecutionPromptContains(t, a.ctxMgr.SystemPrompt().Content, planPath)

	select {
	case <-a.eventCh:
	case <-time.After(2 * time.Second):
		// The execute path may continue retrying after the first spawned LLM goroutine
		// under some test/provider combinations; prompt persistence is the behavior under test.
	}
}

func TestStartPlanExecutionPromptUsesGenericPlanExecutionModeWithDelegateAvailable(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := filepath.Join(projectRoot, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n- task\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "coder", Description: "Coder role"}}}))
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: "primary", Description: "Builder role"},
		"coder":   {Name: "coder", Mode: "subagent", Description: "Coder role"},
	})
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		client := llm.NewClient(providerCfg, stubProvider{response: &message.Response{Content: "ok", StopReason: "stop"}}, "test-model", 1024, "")
		return client, "test-model", 8192, nil
	})

	a.startPlanExecution(planPath, "builder")
	got := a.ctxMgr.SystemPrompt().Content
	for _, want := range []string{
		"## Execution Mode — Plan Execution",
		"visible tools and coordination mechanisms available in",
		"this role",
		"Choose the execution strategy that fits this role",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("execution prompt missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{
		"## Execution Mode — Delegate Orchestrator",
		"## Execution Mode — Direct Plan Execution",
		"Your job is to **delegate** each task",
		"### Available Agent Types",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("generic execution prompt should omit %q in %q", unwanted, got)
		}
	}
}

func TestStartPlanExecutionPromptUsesGenericPlanExecutionModeWhenDelegateUnavailable(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := filepath.Join(projectRoot, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n- task\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {
			Name:        "builder",
			Mode:        "primary",
			Description: "Builder role",
			Permission:  parsePermissionNode(t, "\"*\": deny\nTodoWrite: allow\n"),
		},
		"coder": {Name: "coder", Mode: "subagent", Description: "Coder role"},
	})
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		client := llm.NewClient(providerCfg, stubProvider{response: &message.Response{Content: "ok", StopReason: "stop"}}, "test-model", 1024, "")
		return client, "test-model", 8192, nil
	})

	a.startPlanExecution(planPath, "builder")
	got := a.ctxMgr.SystemPrompt().Content
	for _, want := range []string{
		"## Execution Mode — Plan Execution",
		"visible tools and coordination mechanisms available in",
		"this role",
		"Choose the execution strategy that fits this role",
		"explain the blocker instead of assuming hidden capabilities",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("direct execution prompt missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{
		"## Execution Mode — Delegate Orchestrator",
		"## Execution Mode — Direct Plan Execution",
		"Your job is to **delegate** each task",
		"implement the plan directly using the visible tools for this role",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("generic execution prompt should omit %q in %q", unwanted, got)
		}
	}

	msgs := a.GetMessages()
	if len(msgs) == 0 {
		t.Fatal("expected execution bootstrap message")
	}
	last := msgs[len(msgs)-1].Content
	if strings.Contains(last, "Delegate tool") || strings.Contains(last, "execute the tasks directly") {
		t.Fatalf("bootstrap execution message should stay generic: %q", last)
	}
	if !strings.Contains(last, "execute the plan using the visible tools and coordination mechanisms available in this role") {
		t.Fatalf("bootstrap execution message missing generic execution instruction: %q", last)
	}
}

func TestSetAgentConfigsPreservesExistingActiveRole(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.activeConfig = &config.AgentConfig{Name: "planner", Mode: "primary"}

	builderCfg := &config.AgentConfig{Name: "builder", Mode: "primary"}
	plannerCfg := &config.AgentConfig{Name: "planner", Mode: "primary"}
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": builderCfg,
		"planner": plannerCfg,
	})

	if got := a.CurrentRole(); got != "planner" {
		t.Fatalf("CurrentRole() = %q, want planner", got)
	}
	if got := a.CurrentRoleConfig(); got != plannerCfg {
		t.Fatalf("CurrentRoleConfig() = %p, want %p", got, plannerCfg)
	}
}

func TestEnsureMainModelPolicyWaitsForConcurrentBuild(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("test/test-model")

	replacement := newTestLLMClient()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	waited := make(chan error, 1)
	prewarmDone := make(chan error, 1)
	var builds atomic.Int32

	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "test/test-model" {
			t.Fatalf("providerModel = %q, want test/test-model", providerModel)
		}
		builds.Add(1)
		started <- struct{}{}
		<-release
		return replacement, "replacement-model", 16384, nil
	})

	go func() {
		prewarmDone <- a.PrewarmModelPolicy()
	}()
	<-started

	go func() {
		waited <- a.ensureMainModelPolicy()
	}()

	select {
	case err := <-waited:
		t.Fatalf("ensureMainModelPolicy returned before in-flight build finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	if err := <-prewarmDone; err != nil {
		t.Fatalf("PrewarmModelPolicy: %v", err)
	}
	if err := <-waited; err != nil {
		t.Fatalf("ensureMainModelPolicy: %v", err)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("modelSwitchFactory called %d times, want 1", got)
	}

	a.llmMu.RLock()
	gotClient := a.llmClient
	gotRef := a.providerModelRef
	a.llmMu.RUnlock()
	if gotClient != replacement {
		t.Fatal("expected llmClient to be swapped to replacement client")
	}
	if gotRef != "test/test-model" {
		t.Fatalf("providerModelRef = %q, want test/test-model", gotRef)
	}
}

func TestCurrentRateLimitSnapshotHidesNonCodexProvider(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig = &config.Config{Providers: map[string]config.ProviderConfig{
		"sample": {Type: config.ProviderTypeChatCompletions},
	}}
	a.SetProviderModelRef("sample/model-a")
	a.rateLimitSnaps = map[string]*ratelimit.KeyRateLimitSnapshot{
		"sample": {
			Provider:   "sample",
			CapturedAt: time.Now(),
			Primary:    &ratelimit.RateLimitWindow{UsedPct: 100},
		},
	}

	if got := a.CurrentRateLimitSnapshot(); got != nil {
		t.Fatalf("CurrentRateLimitSnapshot() = %#v, want nil for non-codex provider", got)
	}
}

func TestCurrentRateLimitSnapshotReusesCodexSnapshotAcrossSwitchBack(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig = &config.Config{Providers: map[string]config.ProviderConfig{
		"sample": {Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex},
		"other":  {Type: config.ProviderTypeChatCompletions},
	}}
	snap := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "sample",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 88},
	}
	a.rateLimitSnaps = map[string]*ratelimit.KeyRateLimitSnapshot{"sample": snap}

	a.SetProviderModelRef("sample/model-a")
	if got := a.CurrentRateLimitSnapshot(); got != snap {
		t.Fatalf("CurrentRateLimitSnapshot() = %#v, want %#v on codex provider", got, snap)
	}

	a.llmMu.Lock()
	a.providerModelRef = "other/model-b"
	a.runningModelRef = "other/model-b"
	a.llmMu.Unlock()
	if got := a.CurrentRateLimitSnapshot(); got != nil {
		t.Fatalf("CurrentRateLimitSnapshot() = %#v, want nil after switching to non-codex provider", got)
	}

	a.llmMu.Lock()
	a.providerModelRef = "sample/model-c"
	a.runningModelRef = "sample/model-c"
	a.llmMu.Unlock()
	if got := a.CurrentRateLimitSnapshot(); got != snap {
		t.Fatalf("CurrentRateLimitSnapshot() = %#v, want reused snapshot %#v after switching back", got, snap)
	}
}

func TestCurrentRateLimitSnapshotFallsBackToCurrentOAuthKeySnapshot(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig = &config.Config{Providers: map[string]config.ProviderConfig{
		"openai": {
			Type:   config.ProviderTypeChatCompletions,
			Preset: config.ProviderPresetCodex,
			Models: map[string]config.ModelConfig{
				"gpt-5.5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		},
	}}
	snap := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "openai",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 73},
	}
	providerCfg := llm.NewProviderConfig("openai", a.globalConfig.Providers["openai"], []string{"oauth-key-1", "oauth-key-2"})
	providerCfg.UpdateKeySnapshot("oauth-key-2", snap)
	providerCfg.MarkCooldown("oauth-key-1", time.Minute)
	if selected, _, err := providerCfg.SelectKeyWithContext(context.Background()); err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	} else if selected != "oauth-key-2" {
		t.Fatalf("selected key = %q, want oauth-key-2", selected)
	}
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "gpt-5.5", 1024, "")
	a.SetProviderModelRef("openai/gpt-5.5")

	if got := a.CurrentRateLimitSnapshot(); got != snap {
		t.Fatalf("CurrentRateLimitSnapshot() = %#v, want selected-key snapshot %#v", got, snap)
	}
}

func TestSwitchModelKeepsStoredRateLimitSnapshotsAcrossProviders(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig = &config.Config{Providers: map[string]config.ProviderConfig{
		"sample":  {Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex},
		"wsocket": {Type: config.ProviderTypeChatCompletions},
	}}
	snap := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "sample",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 100},
	}
	a.rateLimitSnaps = map[string]*ratelimit.KeyRateLimitSnapshot{"sample": snap}
	a.SetProviderModelRef("sample/model-a")

	replacement := newTestLLMClient()
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "wsocket/gpt-5.5" {
			t.Fatalf("providerModel = %q, want wsocket/gpt-5.5", providerModel)
		}
		return replacement, "gpt-5.5", 16384, nil
	})

	if err := a.SwitchModel("wsocket/gpt-5.5"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if got := a.CurrentRateLimitSnapshot(); got != nil {
		t.Fatalf("CurrentRateLimitSnapshot() = %#v, want nil after switching away from codex provider", got)
	}
	if got := a.rateLimitSnaps["sample"]; got != snap {
		t.Fatalf("stored sample snapshot = %#v, want %#v", got, snap)
	}

	foundRateClear := false
	for {
		select {
		case evt := <-a.Events():
			switch e := evt.(type) {
			case RateLimitUpdatedEvent:
				if e.Snapshot == nil {
					foundRateClear = true
				}
			}
		default:
			if foundRateClear {
				t.Fatal("did not expect RateLimitUpdatedEvent with nil snapshot on model switch")
			}
			return
		}
	}
}

func TestEnsureMainModelPolicyKeepsRateLimitSnapshotWithinSameProvider(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig = &config.Config{Providers: map[string]config.ProviderConfig{
		"oauth": {Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex},
	}}
	a.SetProviderModelRef("oauth/model-a")
	snap := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "oauth",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 90},
	}
	a.rateLimitSnaps = map[string]*ratelimit.KeyRateLimitSnapshot{"oauth": snap}

	replacement := newTestLLMClient()
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "oauth/model-b" {
			t.Fatalf("providerModel = %q, want oauth/model-b", providerModel)
		}
		return replacement, "model-b", 16384, nil
	})

	if err := a.SwitchModel("oauth/model-b"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if got := a.CurrentRateLimitSnapshot(); got != snap {
		t.Fatalf("CurrentRateLimitSnapshot() = %#v, want original snapshot %#v", got, snap)
	}

	for {
		select {
		case evt := <-a.Events():
			switch e := evt.(type) {
			case RateLimitUpdatedEvent:
				if e.Snapshot == nil {
					t.Fatal("did not expect nil RateLimitUpdatedEvent when provider stays the same")
				}
			}
		default:
			return
		}
	}
}

func newTestMainAgent(t *testing.T, projectRoot string) *MainAgent {
	t.Helper()
	sessionDir := filepath.Join(projectRoot, ".chord", "sessions", "test")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	a := NewMainAgent(
		context.Background(),
		newTestLLMClient(),
		ctxmgr.NewManager(8192, false, 0),
		tools.NewRegistry(),
		&hook.NoopEngine{},
		sessionDir,
		"test-model",
		projectRoot,
		&config.Config{},
		nil,
		mcp.ClientInfo{Name: "chord-test", Version: "test"},
	)
	a.startPersistLoop()
	if a.usageLedger == nil {
		t.Fatal("NewMainAgent should initialize usageLedger for tests")
	}
	t.Cleanup(func() {
		// Ensure all background workers (compaction/persist) are stopped before
		// TempDir cleanup starts, otherwise compaction history exports can race with
		// RemoveAll and cause "directory not empty" failures on macOS.
		a.cancel()
		_ = a.Shutdown(2 * time.Second)
		if a.recovery != nil {
			a.recovery.Close()
		}
	})
	return a
}

func TestSwitchModelAcceptsInlineVariant(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("sample/model-a")

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-b": {
				Limit: config.ModelLimit{Context: 16384, Output: 2048},
				Variants: map[string]config.ModelVariant{
					"high": {Reasoning: &config.ReasoningConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"test-key"})
	client := llm.NewClient(providerCfg, stubProvider{}, "model-b", 2048, "")

	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "sample/model-b@high" {
			t.Fatalf("providerModel = %q, want sample/model-b@high", providerModel)
		}
		client.SetVariant("high")
		return client, "model-b", 16384, nil
	})

	if err := a.SwitchModel("sample/model-b@high"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if got := a.ProviderModelRef(); got != "sample/model-b@high" {
		t.Fatalf("ProviderModelRef = %q, want sample/model-b@high", got)
	}
	if got := a.RunningVariant(); got != "high" {
		t.Fatalf("RunningVariant = %q, want high", got)
	}
}

func TestSwitchModelPropagatesCurrentSessionIDToNewClient(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("sample/model-a")

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-b": {
				Limit: config.ModelLimit{Context: 16384, Output: 2048},
			},
		},
	}, []string{"test-key"})
	providerImpl := &sessionAwareStubProvider{}
	client := llm.NewClient(providerCfg, providerImpl, "model-b", 2048, "")

	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "sample/model-b" {
			t.Fatalf("providerModel = %q, want sample/model-b", providerModel)
		}
		return client, "model-b", 16384, nil
	})

	if err := a.SwitchModel("sample/model-b"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if got := providerImpl.sessionID; got != "test" {
		t.Fatalf("provider sessionID = %q, want test", got)
	}
}

func TestSwitchRoleAppliesPrimaryRoleModelImmediately(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {
			Name:   "builder",
			Mode:   "primary",
			Models: []string{"build/one", "build/two"},
		},
		"executor": {
			Name:   "executor",
			Mode:   "primary",
			Models: []string{"exec/one", "exec/two"},
		},
	})
	a.SetProviderModelRef("build/one")
	a.llmMu.Lock()
	a.runningModelRef = "build/one"
	a.llmMu.Unlock()

	buildClient := newRoleSwitchClient(t, "build", "one", 8192, "build-key")
	execClient := newRoleSwitchClient(t, "exec", "one", 16384, "exec-key")
	var seen []string
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		seen = append(seen, providerModel)
		switch providerModel {
		case "exec/one":
			return execClient, "one", 16384, nil
		case "build/one":
			return buildClient, "one", 8192, nil
		default:
			t.Fatalf("unexpected providerModel %q", providerModel)
			return nil, "", 0, nil
		}
	})

	if err := a.switchRole("executor", false); err != nil {
		t.Fatalf("switchRole: %v", err)
	}
	if got := a.ProviderModelRef(); got != "exec/one" {
		t.Fatalf("ProviderModelRef = %q, want exec/one", got)
	}
	if got := a.RunningModelRef(); got != "exec/one" {
		t.Fatalf("RunningModelRef = %q, want exec/one", got)
	}
	if got := a.CurrentRoleModelRefs(); len(got) != 2 || got[0] != "exec/one" || got[1] != "exec/two" {
		t.Fatalf("CurrentRoleModelRefs = %#v, want exec models", got)
	}
	if a.mainModelPolicyDirty.Load() {
		t.Fatal("mainModelPolicyDirty = true, want false after immediate role model apply")
	}
	if len(seen) != 1 || seen[0] != "exec/one" {
		t.Fatalf("modelSwitchFactory calls = %#v, want [exec/one]", seen)
	}
	if confirmed, total := a.KeyStats(); confirmed != 1 || total != 1 {
		t.Fatalf("KeyStats = %d/%d, want 1/1 for executor provider", confirmed, total)
	}

	foundRunning := false
	for {
		select {
		case evt := <-a.Events():
			switch e := evt.(type) {
			case RunningModelChangedEvent:
				if e.ProviderModelRef == "exec/one" && e.RunningModelRef == "exec/one" {
					foundRunning = true
				}
			}
		default:
			if !foundRunning {
				t.Fatal("missing RunningModelChangedEvent for role model apply")
			}
			return
		}
	}
}

func TestAvailableRolesSortsCustomPrimaryRolesDeterministically(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder":  {Name: "builder", Mode: "primary"},
		"planner":  {Name: "planner", Mode: "primary"},
		"zeta":     {Name: "zeta", Mode: "primary"},
		"alpha":    {Name: "alpha", Mode: "primary"},
		"reviewer": {Name: "reviewer", Mode: "primary"},
		"worker":   {Name: "worker", Mode: "subagent", Models: []string{"worker/one"}},
	})

	got := a.AvailableRoles()
	want := []string{"builder", "planner", "alpha", "reviewer", "zeta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("AvailableRoles = %#v, want %#v", got, want)
	}
}

func TestAvailableAgentsSortsNonBuilderRolesDeterministically(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: "primary"},
		"planner": {Name: "planner", Mode: "primary"},
		"zeta":    {Name: "zeta", Mode: "primary"},
		"alpha":   {Name: "alpha", Mode: "primary"},
		"worker":  {Name: "worker", Mode: "subagent", Models: []string{"worker/one"}},
	})
	if err := a.switchRole("planner", false); err != nil {
		t.Fatalf("switchRole: %v", err)
	}

	got := a.AvailableAgents()
	want := []string{"builder", "alpha", "zeta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("AvailableAgents = %#v, want %#v", got, want)
	}
}

func TestSwitchRoleAppliesRoleModelWithoutToast(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder":  {Name: "builder", Mode: "primary", Models: []string{"build/one"}},
		"executor": {Name: "executor", Mode: "primary", Models: []string{"exec/one"}},
	})
	a.SetProviderModelRef("build/one")

	execClient := newRoleSwitchClient(t, "exec", "one", 16384, "exec-key")
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "exec/one" {
			t.Fatalf("providerModel = %q, want exec/one", providerModel)
		}
		return execClient, "one", 16384, nil
	})

	a.SwitchRole("executor")

	seenRunning := false
	for {
		select {
		case evt := <-a.Events():
			switch e := evt.(type) {
			case RunningModelChangedEvent:
				if e.ProviderModelRef == "exec/one" && e.RunningModelRef == "exec/one" {
					seenRunning = true
				}
			case ToastEvent:
				t.Fatalf("unexpected toast during role switch: %#v", e)
			}
		default:
			if !seenRunning {
				t.Fatal("missing RunningModelChangedEvent for SwitchRole")
			}
			if got := a.ProviderModelRef(); got != "exec/one" {
				t.Fatalf("ProviderModelRef = %q, want exec/one", got)
			}
			return
		}
	}
}

func TestSwitchRoleEmitsRoleChangedEvent(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder":  {Name: "builder", Mode: "primary", Models: []string{"build/one"}},
		"executor": {Name: "executor", Mode: "primary", Models: []string{"exec/one"}},
	})
	a.SetProviderModelRef("build/one")

	execClient := newRoleSwitchClient(t, "exec", "one", 16384, "exec-key")
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "exec/one" {
			t.Fatalf("providerModel = %q, want exec/one", providerModel)
		}
		return execClient, "one", 16384, nil
	})

	a.SwitchRole("executor")

	seenRoleChanged := false
	for {
		select {
		case evt := <-a.Events():
			switch e := evt.(type) {
			case RoleChangedEvent:
				if e.Role == "executor" {
					seenRoleChanged = true
				}
			}
		default:
			if !seenRoleChanged {
				t.Fatal("missing RoleChangedEvent for SwitchRole")
			}
			if got := a.CurrentRole(); got != "executor" {
				t.Fatalf("CurrentRole = %q, want executor", got)
			}
			return
		}
	}
}

func TestSwitchRoleUsesAgentVariantForPrimaryRoleModel(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: "primary", Models: []string{"build/one"}},
		"executor": {
			Name:    "executor",
			Mode:    "primary",
			Models:  []string{"exec/one"},
			Variant: "high",
		},
	})

	execClient := newRoleSwitchClient(t, "exec", "one", 16384, "exec-key")
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "exec/one@high" {
			t.Fatalf("providerModel = %q, want exec/one@high", providerModel)
		}
		execClient.SetVariant("high")
		return execClient, "one", 16384, nil
	})

	if err := a.switchRole("executor", false); err != nil {
		t.Fatalf("switchRole: %v", err)
	}
	if got := a.ProviderModelRef(); got != "exec/one@high" {
		t.Fatalf("ProviderModelRef = %q, want exec/one@high", got)
	}
	if got := a.RunningVariant(); got != "high" {
		t.Fatalf("RunningVariant = %q, want high", got)
	}
}

func TestSwitchRoleWithNoModelsLeavesSelectedModelUntouchedAndDefersPolicy(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder":  {Name: "builder", Mode: "primary", Models: []string{"build/one"}},
		"reviewer": {Name: "reviewer", Mode: "primary"},
	})
	a.SetProviderModelRef("build/one")
	a.llmMu.Lock()
	a.runningModelRef = "build/one"
	a.llmMu.Unlock()

	called := false
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		called = true
		return newRoleSwitchClient(t, "build", "one", 8192, "build-key"), "one", 8192, nil
	})

	if err := a.switchRole("reviewer", false); err != nil {
		t.Fatalf("switchRole: %v", err)
	}
	if called {
		t.Fatal("modelSwitchFactory called for role without explicit models")
	}
	if got := a.ProviderModelRef(); got != "build/one" {
		t.Fatalf("ProviderModelRef = %q, want build/one", got)
	}
	if !a.mainModelPolicyDirty.Load() {
		t.Fatal("mainModelPolicyDirty = false, want true when role has no explicit models")
	}
}

func TestRunningModelRefAndKeyStatsFollowFocusedSubAgent(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetProviderModelRef("mainp/mainm")
	// Match production: SetProviderModelRef does not rewrite an already-set runningModelRef from NewMainAgent.
	a.llmMu.Lock()
	a.runningModelRef = "mainp/mainm"
	a.llmMu.Unlock()

	subProv := llm.NewProviderConfig("subp", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"subm": {Limit: config.ModelLimit{Context: 100, Output: 10}},
		},
	}, []string{"k1", "k2", "k3"})
	subClient := llm.NewClient(subProv, stubProvider{}, "subm", 10, "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "sub-1",
		llmClient:  subClient,
		parent:     a,
		parentCtx:  context.Background(),
		cancel:     cancel,
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, false, 0),
		turn: &Turn{
			ID:              1,
			Ctx:             ctx,
			Cancel:          cancel,
			PendingToolMeta: make(map[string]PendingToolCall),
		},
	}
	a.mu.Lock()
	a.subAgents["sub-1"] = sub
	a.mu.Unlock()
	a.focusedAgent.Store(sub)

	if got := a.RunningModelRef(); got != "subp/subm" {
		t.Fatalf("RunningModelRef = %q, want subp/subm", got)
	}
	avail, tot := a.KeyStats()
	if tot != 3 || avail != 3 {
		t.Fatalf("KeyStats = %d/%d, want 3/3", avail, tot)
	}

	a.focusedAgent.Store(nil)
	if got := a.RunningModelRef(); got != "mainp/mainm" {
		t.Fatalf("after unfocus RunningModelRef = %q, want mainp/mainm", got)
	}
	if got := a.ProviderModelRef(); got != "mainp/mainm" {
		t.Fatalf("ProviderModelRef = %q, want mainp/mainm (still main)", got)
	}
}

func newTestLLMClient() *llm.Client {
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
	return llm.NewClient(providerCfg, stubProvider{}, "test-model", 1024, "")
}

func newRoleSwitchClient(t *testing.T, providerName, modelID string, contextLimit int, keys ...string) *llm.Client {
	t.Helper()
	if len(keys) == 0 {
		keys = []string{"test-key"}
	}
	providerCfg := llm.NewProviderConfig(providerName, config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			modelID: {
				Limit: config.ModelLimit{Context: contextLimit, Output: 1024},
				Variants: map[string]config.ModelVariant{
					"high": {Reasoning: &config.ReasoningConfig{Effort: "high"}},
				},
			},
		},
	}, keys)
	return llm.NewClient(providerCfg, stubProvider{}, modelID, 1024, "")
}

func TestStartPlanExecutionPromptIncludesOrchestrationRulesWithDelegate(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := filepath.Join(projectRoot, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n- task\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "coder", Description: "Coder role"}}}))
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: "primary", Description: "Builder role"},
		"coder":   {Name: "coder", Mode: "subagent", Description: "Coder role"},
	})
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		client := llm.NewClient(providerCfg, stubProvider{response: &message.Response{Content: "ok", StopReason: "stop"}}, "test-model", 1024, "")
		return client, "test-model", 8192, nil
	})

	a.startPlanExecution(planPath, "builder")
	got := a.ctxMgr.SystemPrompt().Content

	// Verify orchestration rules for parallel dispatch and non-takeover
	for _, want := range []string{
		"first dispatch all currently independent tasks whose write scopes are clearly disjoint",
		"Dispatch tasks in parallel only when their write scopes are clearly independent",
		"no new independent task to send, stop doing implementation work in MainAgent",
		"do not take over implementation just because a SubAgent is briefly quiet",
		"has not written files yet, or has not produced immediate visible output",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("execution prompt with Delegate should include orchestration rule %q in %q", want, got)
		}
	}
}

func TestStartPlanExecutionPromptIncludesOrchestrationRulesWithoutTodoWrite(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := filepath.Join(projectRoot, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n- task\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "coder", Description: "Coder role"}}}))
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {
			Name:        "builder",
			Mode:        "primary",
			Description: "Builder role",
			Permission:  parsePermissionNode(t, "\"*\": deny\nDelegate: allow\n"),
		},
		"coder": {Name: "coder", Mode: "subagent", Description: "Coder role"},
	})
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		client := llm.NewClient(providerCfg, stubProvider{response: &message.Response{Content: "ok", StopReason: "stop"}}, "test-model", 1024, "")
		return client, "test-model", 8192, nil
	})

	a.startPlanExecution(planPath, "builder")
	got := a.ctxMgr.SystemPrompt().Content

	// Without TodoWrite, the prompt should still include orchestration rules
	for _, want := range []string{
		"first dispatch all currently independent tasks whose write scopes are clearly disjoint",
		"Dispatch tasks in parallel only when their write scopes are clearly independent",
		"no new independent task to send, stop doing implementation work in MainAgent",
		"do not take over implementation just because a SubAgent is briefly quiet",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("execution prompt without TodoWrite should include orchestration rule %q in %q", want, got)
		}
	}
}

func TestStartPlanExecutionLoopAssessmentWaitsForActiveSubAgentSignals(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := filepath.Join(projectRoot, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n- task\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "coder", Description: "Coder role"}}}))
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: "primary", Description: "Builder role"},
		"coder":   {Name: "coder", Mode: "subagent", Description: "Coder role"},
	})
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		client := llm.NewClient(providerCfg, stubProvider{response: &message.Response{Content: "ok", StopReason: "stop"}}, "test-model", 1024, "")
		return client, "test-model", 8192, nil
	})

	// Execute-plan path should bootstrap a plan execution turn.
	a.startPlanExecution(planPath, "builder")
	msgs := a.GetMessages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "Execute the plan at") {
		t.Fatalf("expected execute-plan bootstrap message, got %#v", msgs)
	}

	// Simulate a delegated worker still running (no Complete/Escalate/error/blocked signal yet).
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := &SubAgent{
		instanceID: "worker-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		inputCh:    make(chan pendingUserMessage, 1),
		recovery:   a.recovery,
		ctxMgr:     ctxmgr.NewManager(100, false, 0),
	}
	a.mu.Lock()
	a.subAgents[sub.instanceID] = sub
	a.mu.Unlock()

	a.loopState.enableWithTarget("execute the active plan")
	a.loopState.markProgress()
	assessment := a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "delegated implementation is done\n<done>all tasks done</done>",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want continue assessment while subagent is still active")
	}
	if assessment.Action != LoopAssessmentActionContinue {
		t.Fatalf("assessment.Action = %q, want %q while worker active", assessment.Action, LoopAssessmentActionContinue)
	}
	if !strings.Contains(assessment.Message, "active subagents must finish before completion") {
		t.Fatalf("assessment.Message = %q, want active-subagent completion guard", assessment.Message)
	}
	hasSubagentGuard := false
	for _, reason := range assessment.Reasons {
		if reason == "subagents_active" {
			hasSubagentGuard = true
			break
		}
	}
	if !hasSubagentGuard {
		t.Fatalf("assessment.Reasons = %v, want subagents_active", assessment.Reasons)
	}

	// Once the worker is no longer active, the same done-tag signal can complete.
	a.mu.Lock()
	delete(a.subAgents, sub.instanceID)
	a.mu.Unlock()

	a.loopState.markProgress()
	assessment = a.nextLoopAssessmentFromAssistant(message.Message{
		Role:       "assistant",
		Content:    "all delegated work finished\n<verify-not-run>validation harness is unavailable in this test</verify-not-run>\n<done>all tasks done</done>",
		StopReason: "stop",
	})
	if assessment == nil {
		t.Fatal("assessment = nil, want completed assessment after worker finishes")
	}
	if assessment.Action != LoopAssessmentActionCompleted {
		t.Fatalf("assessment.Action = %q, want %q after worker completion", assessment.Action, LoopAssessmentActionCompleted)
	}
}

func assertExecutionPromptContains(t *testing.T, prompt, planPath string) {
	t.Helper()
	if !strings.Contains(prompt, "## Execution Mode") {
		t.Fatalf("system prompt missing execution mode block:\n%s", prompt)
	}
	if !strings.Contains(prompt, planPath) {
		t.Fatalf("system prompt missing plan path %q:\n%s", planPath, prompt)
	}
}
