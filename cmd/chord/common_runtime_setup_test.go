package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestCreateRuntimeRequiresMainAgent(t *testing.T) {
	rt, err := createRuntime(&AppContext{Registry: tools.NewRegistry()})
	if err == nil || rt != nil {
		t.Fatalf("createRuntime() = (%v, %v), want nil runtime and error", rt, err)
	}
}

func TestCreateRuntimeRequiresRegistry(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Registry = nil
	rt, err := createRuntime(ac)
	if err == nil || rt != nil {
		t.Fatalf("createRuntime() = (%v, %v), want nil runtime and error", rt, err)
	}
}

func TestCreateRuntimeWiresConfirmAndQuestionTools(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Ctx, ac.Cancel = context.WithCancel(context.Background())
	defer ac.Cancel()
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{}

	rt, err := createRuntime(ac)
	if err != nil {
		t.Fatalf("createRuntime: %v", err)
	}
	defer rt.Close()

	if rt.Agent != ac.MainAgent {
		t.Fatal("runtime agent does not reference app context main agent")
	}
	if _, ok := ac.Registry.Get(tools.NameQuestion); !ok {
		t.Fatal("question tool was not registered into runtime registry")
	}

	confirmDone := make(chan error, 1)
	go func() {
		_, err := ac.MainAgent.AwaitConfirm(context.Background(), "Delete", `{}`, time.Second, nil, nil)
		confirmDone <- err
	}()
	confirmReq := waitForConfirmRequestEvent(t, ac.MainAgent.Events())
	ac.MainAgent.ResolveConfirm("allow", `{}`, "", "", confirmReq.RequestID)
	if err := <-confirmDone; err != nil {
		t.Fatalf("AwaitConfirm via runtime wiring: %v", err)
	}

	questionDone := make(chan error, 1)
	go func() {
		_, err := ac.Registry.Execute(context.Background(), tools.NameQuestion, []byte(`{"questions":[{"header":"h","question":"q","options":[{"label":"yes","description":"y"}]}]}`))
		questionDone <- err
	}()
	questionReq := waitForQuestionRequestEvent(t, ac.MainAgent.Events())
	ac.MainAgent.ResolveQuestion([]string{"yes"}, false, questionReq.RequestID)
	if err := <-questionDone; err != nil {
		t.Fatalf("Question tool via runtime wiring: %v", err)
	}
}

func TestRuntimeCloseIsNilSafe(t *testing.T) {
	(&Runtime{}).Close()
}

func TestAppContextCleanupWaitsForProviderBackgroundTask(t *testing.T) {
	provider := llm.NewProviderConfig("sample", config.ProviderConfig{}, nil)
	releaseTask := make(chan struct{})
	if !provider.StartBackgroundTask(func() { <-releaseTask }) {
		t.Fatal("StartBackgroundTask returned false")
	}
	ctx, cancel := context.WithCancel(t.Context())
	ac := &AppContext{
		Ctx:    ctx,
		Cancel: cancel,
		ProviderCache: &providerCache{m: map[string]*llm.ProviderConfig{
			"sample": provider,
		}},
	}
	cleanupDone := make(chan struct{})
	go func() {
		ac.cleanup()
		close(cleanupDone)
	}()
	select {
	case <-cleanupDone:
		t.Fatal("cleanup returned before provider background task completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseTask)
	select {
	case <-cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not return after provider background task completed")
	}
}

func TestEnsureRuntimeLSPNoopsWithoutConfig(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{}
	ensureRuntimeLSP(ac)
	if ac.LSPManager != nil {
		t.Fatal("LSP manager should stay nil without LSP config")
	}
	if _, ok := ac.Registry.Get(tools.NameLsp); ok {
		t.Fatal("LSP tool should not be registered without LSP config")
	}
}

func TestEnsureRuntimeLSPRegistersLSPAwareTools(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{
		LSP: config.LSPConfig{
			"sample-lsp": {
				Command:   "sample-lsp",
				FileTypes: []string{"go"},
			},
		},
	}
	ensureRuntimeLSP(ac)
	if ac.LSPManager == nil {
		t.Fatal("LSP manager was not initialized")
	}
	for _, name := range []string{tools.NameRead, tools.NameWrite, tools.NameEdit, tools.NameDelete, tools.NameLsp} {
		if _, ok := ac.Registry.Get(name); !ok {
			t.Fatalf("tool %s was not registered", name)
		}
	}
}

func TestEnsureRuntimeLSPKeepsExistingManager(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{
		LSP: config.LSPConfig{
			"sample-lsp": {Command: "sample-lsp"},
		},
	}
	ensureRuntimeLSP(ac)
	existing := ac.LSPManager
	ensureRuntimeLSP(ac)
	if ac.LSPManager != existing {
		t.Fatal("ensureRuntimeLSP replaced an existing manager")
	}
}

func TestStartRuntimeMCPNoopsWhenRuntimeIsIncomplete(t *testing.T) {
	tests := []struct {
		name string
		ac   *AppContext
	}{
		{name: "nil app context"},
		{name: "missing agent", ac: &AppContext{Registry: tools.NewRegistry()}},
		{name: "missing registry", ac: &AppContext{MainAgent: newTestAppContext(t).MainAgent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startRuntimeMCP(tt.ac)
		})
	}
}

func TestStartRuntimeMCPManualServerMarksDiscoveryReady(t *testing.T) {
	ac := newTestAppContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac.Ctx = ctx
	ac.Cancel = cancel
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{}
	mgr, err := mcp.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ac.MCPMgr = mgr
	ac.MCPConfigs = []mcp.ServerConfig{{Name: "manual", Manual: true}}

	startRuntimeMCP(ac)

	deadline := time.After(2 * time.Second)
	updates := 0
	for updates < 2 {
		select {
		case evt := <-ac.MainAgent.Events():
			if _, ok := evt.(agent.EnvStatusUpdateEvent); ok {
				updates++
			}
		case <-deadline:
			t.Fatalf("timed out waiting for MCP env status updates, got %d", updates)
		}
	}
}

func waitForConfirmRequestEvent(t *testing.T, ch <-chan agent.AgentEvent) agent.ConfirmRequestEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-ch:
			if req, ok := evt.(agent.ConfirmRequestEvent); ok {
				return req
			}
		case <-deadline:
			t.Fatal("timed out waiting for ConfirmRequestEvent")
		}
	}
}

func waitForQuestionRequestEvent(t *testing.T, ch <-chan agent.AgentEvent) agent.QuestionRequestEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-ch:
			if req, ok := evt.(agent.QuestionRequestEvent); ok {
				return req
			}
		case <-deadline:
			t.Fatal("timed out waiting for QuestionRequestEvent")
		}
	}
}

func TestCreateRuntimeQuestionToolRoundTripReturnsAnswers(t *testing.T) {
	ac := newTestAppContext(t)
	ac.Ctx, ac.Cancel = context.WithCancel(context.Background())
	defer ac.Cancel()
	ac.Registry = tools.NewRegistry()
	ac.Cfg = &config.Config{}

	if _, err := createRuntime(ac); err != nil {
		t.Fatalf("createRuntime: %v", err)
	}

	questionDone := make(chan string, 1)
	go func() {
		out, err := ac.Registry.Execute(context.Background(), tools.NameQuestion, []byte(`{"questions":[{"header":"h","question":"q","options":[{"label":"yes","description":"y"}]}]}`))
		if err != nil {
			questionDone <- err.Error()
			return
		}
		questionDone <- out
	}()
	questionReq := waitForQuestionRequestEvent(t, ac.MainAgent.Events())
	ac.MainAgent.ResolveQuestion([]string{"yes"}, false, questionReq.RequestID)
	out := <-questionDone
	var answers []tools.QuestionAnswer
	if err := json.Unmarshal([]byte(out), &answers); err != nil {
		t.Fatalf("unmarshal answers: %v", err)
	}
	if len(answers) != 1 || len(answers[0].Selected) != 1 || answers[0].Selected[0] != "yes" {
		t.Fatalf("answers = %#v, want yes", answers)
	}
}

func TestRestoreMCPIntentReconcilesManualServers(t *testing.T) {
	ac := newTestAppContext(t)
	if err := recovery.SetMCPEnabledServers(ac.SessionDir, []string{"manual-a"}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	ac.MCPConfigs = []mcp.ServerConfig{
		{Name: "auto"},
		{Name: "manual-a", Manual: true},
		{Name: "manual-b", Manual: true},
	}
	ac.MCPMgr = mcp.NewPendingManager(ac.MCPConfigs)
	ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)
	// Simulate manual-b already connected/desired from a previous session.
	ac.MCPCatalog.SetDesiredEnabled("manual-b", true)

	if err := restoreMCPIntent(context.Background(), ac); err != nil {
		t.Fatalf("restoreMCPIntent: %v", err)
	}
	if !ac.MCPCatalog.DesiredEnabled("manual-a") {
		t.Error("manual-a should be desired-enabled from persisted intent")
	}
	if ac.MCPCatalog.DesiredEnabled("manual-b") {
		t.Error("manual-b should be deselected (not in target intent)")
	}
	if !ac.MCPCatalog.DesiredEnabled("auto") {
		t.Error("automatic server should remain enabled")
	}
}

func TestRestoreMCPIntentKeepsIntentOnConnectFailure(t *testing.T) {
	ac := newTestAppContext(t)
	if err := recovery.SetMCPEnabledServers(ac.SessionDir, []string{"manual-a"}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	// No Command/URL: ConnectOne fails immediately, but the desired intent must
	// survive so a later retry can succeed.
	ac.MCPConfigs = []mcp.ServerConfig{{Name: "manual-a", Manual: true}}
	ac.MCPMgr = mcp.NewPendingManager(ac.MCPConfigs)
	ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)

	if err := restoreMCPIntent(context.Background(), ac); err != nil {
		t.Fatalf("restoreMCPIntent: %v", err)
	}
	if !ac.MCPCatalog.DesiredEnabled("manual-a") {
		t.Fatal("connect failure must not clear desired-enabled intent")
	}
	if ac.MCPMgr.Client("manual-a") != nil {
		t.Fatal("failed server must not have a client")
	}
}

func TestLoadSynchronousMCPStateRestoresManualIntent(t *testing.T) {
	ac := newTestAppContext(t)
	if err := recovery.SetMCPEnabledServers(ac.SessionDir, []string{"manual-a"}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	ac.MCPConfigs = []mcp.ServerConfig{{Name: "manual-a", Manual: true}}
	ac.MCPMgr = mcp.NewPendingManager(ac.MCPConfigs)
	ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)

	result, err := loadSynchronousMCPState(context.Background(), ac)
	if err != nil {
		t.Fatalf("loadSynchronousMCPState: %v", err)
	}
	if !ac.MCPCatalog.DesiredEnabled("manual-a") {
		t.Fatal("synchronous startup did not restore manual MCP intent")
	}
	if len(result.Tools) != 0 || result.PromptBlock != "" {
		t.Fatalf("failed manual server state = %#v, want no connected surface", result)
	}
}

func TestRestoreMCPIntentReadFailureKeepsCurrentState(t *testing.T) {
	ac := newTestAppContext(t)
	if err := recovery.SaveSessionMeta(ac.SessionDir, recovery.SessionMeta{MCPEnabledServers: []string{"manual-a"}}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	ac.MCPConfigs = []mcp.ServerConfig{{Name: "manual-a", Manual: true}}
	ac.MCPMgr = mcp.NewPendingManager(ac.MCPConfigs)
	ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)
	ac.MCPCatalog.SetDesiredEnabled("manual-a", true)
	if err := os.WriteFile(filepath.Join(ac.SessionDir, "session-meta.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("corrupt meta: %v", err)
	}

	err := restoreMCPIntent(context.Background(), ac)
	if err == nil || !strings.Contains(err.Error(), "load MCP session intent") {
		t.Fatalf("restoreMCPIntent error = %v, want metadata read failure", err)
	}
	if !ac.MCPCatalog.DesiredEnabled("manual-a") {
		t.Fatal("metadata read failure cleared current desired-enabled state")
	}
}

func TestPersistMCPEnabledIntentWritesAndNormalizes(t *testing.T) {
	sessionDir := t.TempDir()
	if err := persistMCPEnabledIntent(sessionDir, []string{" b ", "a", "b"}); err != nil {
		t.Fatalf("persistMCPEnabledIntent: %v", err)
	}
	meta, err := recovery.LoadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta == nil || !slices.Equal(meta.MCPEnabledServers, []string{"a", "b"}) {
		t.Fatalf("persisted MCPEnabledServers = %v, want [a b]", meta)
	}
}

func TestRuntimeMCPControlUsesSessionMetadataInsteadOfSharedCatalog(t *testing.T) {
	ac := newTestAppContext(t)
	if err := recovery.SetMCPEnabledServers(ac.SessionDir, []string{"manual-existing"}); err != nil {
		t.Fatalf("seed session metadata: %v", err)
	}
	ac.MCPConfigs = []mcp.ServerConfig{
		{Name: "manual-existing", Manual: true},
		{Name: "manual-new", Manual: true},
	}
	ac.MCPMgr = mcp.NewPendingManager(ac.MCPConfigs)
	ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)

	_, err := controlRuntimeMCP(context.Background(), ac, agent.MCPControlRequest{
		Action:  agent.MCPControlEnable,
		Servers: []string{"manual-new"},
	})
	if err == nil {
		t.Fatal("expected connection failure for pending MCP server")
	}
	meta, err := recovery.LoadSessionMeta(ac.SessionDir)
	if err != nil {
		t.Fatalf("load session metadata: %v", err)
	}
	if meta == nil || !slices.Equal(meta.MCPEnabledServers, []string{"manual-existing", "manual-new"}) {
		t.Fatalf("persisted MCP intent = %v, want existing and newly enabled servers", meta)
	}
}

func TestRuntimeMCPControlForStaleSessionDoesNotMutateActiveRuntime(t *testing.T) {
	ac := newTestAppContext(t)
	staleSessionDir := t.TempDir()
	if err := recovery.SetMCPEnabledServers(staleSessionDir, []string{"manual-old"}); err != nil {
		t.Fatalf("seed stale session metadata: %v", err)
	}
	if err := recovery.SetMCPEnabledServers(ac.MainAgent.SessionDir(), []string{"manual-active"}); err != nil {
		t.Fatalf("seed active session metadata: %v", err)
	}
	ac.MCPConfigs = []mcp.ServerConfig{
		{Name: "manual-old", Manual: true},
		{Name: "manual-new", Manual: true},
		{Name: "manual-active", Manual: true},
	}
	ac.MCPMgr = mcp.NewPendingManager(ac.MCPConfigs)
	ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)

	result, err := controlRuntimeMCP(context.Background(), ac, agent.MCPControlRequest{
		Action:     agent.MCPControlEnable,
		Servers:    []string{"manual-new"},
		SessionDir: staleSessionDir,
	})
	if err != nil {
		t.Fatalf("stale MCP control: %v", err)
	}
	if len(result.Enabled) != 0 || len(result.Tools) != 0 {
		t.Fatalf("stale MCP control result = %#v, want no active runtime mutation", result)
	}
	if ac.MCPCatalog.DesiredEnabled("manual-new") {
		t.Fatal("stale MCP control changed active catalog intent")
	}
	staleMeta, err := recovery.LoadSessionMeta(staleSessionDir)
	if err != nil {
		t.Fatalf("load stale session metadata: %v", err)
	}
	if staleMeta == nil || !slices.Equal(staleMeta.MCPEnabledServers, []string{"manual-new", "manual-old"}) {
		t.Fatalf("stale session intent = %v, want old and newly enabled servers", staleMeta)
	}
	activeMeta, err := recovery.LoadSessionMeta(ac.MainAgent.SessionDir())
	if err != nil {
		t.Fatalf("load active session metadata: %v", err)
	}
	if activeMeta == nil || !slices.Equal(activeMeta.MCPEnabledServers, []string{"manual-active"}) {
		t.Fatalf("active session intent = %v, want unchanged active intent", activeMeta)
	}
}

func TestBeginMCPRestoreCancelsSupersededGeneration(t *testing.T) {
	ctx := t.Context()
	ac := &AppContext{Ctx: ctx}
	firstGeneration, firstCtx := beginMCPRestore(ac)
	secondGeneration, secondCtx := beginMCPRestore(ac)
	if secondGeneration <= firstGeneration {
		t.Fatalf("restore generations = %d then %d", firstGeneration, secondGeneration)
	}
	select {
	case <-firstCtx.Done():
	default:
		t.Fatal("superseded restore context was not canceled")
	}
	select {
	case <-secondCtx.Done():
		t.Fatal("current restore context was canceled early")
	default:
	}
	finishMCPRestore(ac, firstGeneration)
	select {
	case <-secondCtx.Done():
		t.Fatal("stale finish canceled the current restore")
	default:
	}
	finishMCPRestore(ac, secondGeneration)
	select {
	case <-secondCtx.Done():
	default:
		t.Fatal("current finish did not release its context")
	}
}
