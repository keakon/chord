package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/power"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

// Runtime owns local-mode wiring around a MainAgent. The AppContext still owns
// the agent lifecycle and remains responsible for shutdown via ac.Close().
type Runtime struct {
	Agent    *agent.MainAgent
	powerMgr *power.Manager // nil if prevent_sleep not enabled
}

func createRuntime(ac *AppContext) (*Runtime, error) {
	if ac == nil || ac.MainAgent == nil {
		return nil, fmt.Errorf("runtime requires an initialized main agent")
	}
	if ac.Registry == nil {
		return nil, fmt.Errorf("runtime requires an initialized tool registry")
	}

	ensureRuntimeLSP(ac)
	configureRuntimeStateProviders(ac)

	// Wire power manager if prevent_sleep is enabled.
	var powerMgr *power.Manager
	resourceCtrl := newRuntimeResourceController(ac.Ctx, ac.LSPManager, ac.MCPMgr, nil, ac.MainAgent.NotifyEnvStatusUpdated)
	resourceCtrl.restoreMCP = restoreRuntimeMCP(ac, resourceCtrl)
	ac.RuntimeResources = resourceCtrl
	if ac.Cfg != nil && ac.Cfg.PreventSleep != nil && *ac.Cfg.PreventSleep {
		powerMgr = power.NewManager(power.NewBackend())
		ac.MainAgent.SetActivityObserver(combineActivityObservers(
			&activityObserverAdapter{mgr: powerMgr},
			resourceCtrl,
		))
		log.Debug("prevent_sleep enabled: activity-based sleep prevention active")
	} else {
		ac.MainAgent.SetActivityObserver(resourceCtrl)
	}
	ac.MainAgent.SetBusyPreparationHook(resourceCtrl.EnsureReady)

	confirmTimeout := time.Duration(ac.Cfg.ConfirmTimeout) * time.Second
	wireMainAgentRuntime(ac.Ctx, ac.MainAgent, ac.Registry, confirmTimeout)
	startRuntimeMCP(ac)
	startRuntimeWarmups(ac)

	return &Runtime{Agent: ac.MainAgent, powerMgr: powerMgr}, nil
}

func wireMainAgentRuntime(ctx context.Context, mainAgent *agent.MainAgent, reg *tools.Registry, confirmTimeout time.Duration) {
	mainAgent.SetConfirmFunc(func(ctx context.Context, toolName, args string, needsApproval, alreadyAllowed, needsApprovalRules, alreadyAllowedRules []string) (agent.ConfirmResponse, error) {
		resp, err := mainAgent.AwaitConfirmWithRuleContext(ctx, toolName, args, confirmTimeout, needsApproval, alreadyAllowed, needsApprovalRules, alreadyAllowedRules)
		if err != nil {
			return agent.ConfirmResponse{}, err
		}
		return resp, nil
	})

	reg.Register(tools.NewQuestionTool(func(ctx context.Context, questions []tools.QuestionItem) ([]tools.QuestionAnswer, error) {
		return mainAgent.AskQuestions(ctx, questions, confirmTimeout)
	}))
	reg.Register(tools.NewDoneTool())

	go mainAgent.Run(ctx)
}

func (rt *Runtime) Close() {
	if rt == nil {
		return
	}
	if rt.powerMgr != nil {
		rt.powerMgr.Close()
	}
	if rt.Agent != nil {
		rt.Agent.SetBusyPreparationHook(nil)
	}
	if rt.Agent != nil {
		rt.Agent.ClearPendingInteractions()
	}
}

func (rt *Runtime) WaitIdleOrTimeout(timeout time.Duration) bool {
	if rt == nil || rt.Agent == nil {
		return true
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case evt, ok := <-rt.Agent.Events():
			if !ok {
				return true
			}
			if _, ok := evt.(agent.GlobalIdleEvent); ok {
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

func ensureRuntimeLSP(ac *AppContext) {
	if ac == nil || ac.Cfg == nil || len(ac.Cfg.LSP) == 0 || ac.LSPManager != nil {
		return
	}

	ac.LSPManager = lsp.NewManager(ac.Cfg, ac.ProjectRoot, nil)
	ac.Registry.Register(tools.ReadTool{LSP: ac.LSPManager, BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.WriteTool{LSP: ac.LSPManager, BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.ApplyPatchTool{LSP: ac.LSPManager, BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.EditTool{LSP: ac.LSPManager, BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.DeleteTool{LSP: ac.LSPManager, BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.LspTool{LSP: ac.LSPManager, BaseDir: ac.ProjectRoot})
}

func configureRuntimeStateProviders(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil {
		return
	}

	ac.MainAgent.SetLSPStatusFunc(
		func() []agent.LSPServerDisplay { return lspServerDisplayList(ac.LSPManager, ac.RuntimeResources) },
	)
	ac.MainAgent.SetLSPSessionFuncs(
		func() {
			if ac.LSPManager == nil {
				return
			}
			ac.LSPManager.ResetTouched()
			ac.LSPManager.ResetReviews()
			ac.MainAgent.NotifyEnvStatusUpdated()
		},
		func(msgs []message.Message) {
			if ac.LSPManager == nil {
				return
			}
			ac.LSPManager.RebuildTouchedPaths(agent.RebuildTouchedPathsFromMessages(msgs, ac.ProjectRoot))
			ac.LSPManager.RebuildReviewSnapshots(lsp.RebuildReviewSnapshotsFromMessages(msgs))
			ac.MainAgent.NotifyEnvStatusUpdated()
		},
	)
	ac.MainAgent.SetMCPStatusFunc(func() []agent.MCPServerDisplay {
		return mcpServerDisplayList(ac.MCPMgr, ac.MCPCatalog, ac.RuntimeResources)
	})
	ac.MainAgent.SetMCPKnownToolNamesFunc(func(serverName string) []string {
		if ac.MCPMgr == nil {
			return nil
		}
		return ac.MCPMgr.KnownRegisteredToolNames(serverName)
	})
	ac.MainAgent.SetMCPControlFunc(func(ctx context.Context, req agent.MCPControlRequest) (agent.MCPControlResult, error) {
		return controlRuntimeMCP(ctx, ac, req)
	})
}

func controlRuntimeMCP(ctx context.Context, ac *AppContext, req agent.MCPControlRequest) (agent.MCPControlResult, error) {
	// Ensure MCP startup wiring has run before manual control operations.
	startRuntimeMCP(ac)
	if ac.MCPMgr == nil {
		return agent.MCPControlResult{}, fmt.Errorf("MCP is not configured")
	}
	if ctx == nil {
		ctx = ac.Ctx
	}
	if ac.MCPCatalog == nil {
		ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)
	}
	// MCP connections and the catalog are process-wide, while desired
	// enablement is session-scoped. Serialize controls with startup, restore,
	// and idle reload so a stale control cannot leave the active session with
	// the previous session's connections.
	ac.mcpRestoreRunMu.Lock()
	defer ac.mcpRestoreRunMu.Unlock()

	activeSessionDir := ""
	if ac.MainAgent != nil {
		activeSessionDir = ac.MainAgent.SessionDir()
	}
	intentSessionDir := strings.TrimSpace(req.SessionDir)
	if intentSessionDir == "" {
		intentSessionDir = activeSessionDir
	}
	staleSession := strings.TrimSpace(req.SessionDir) != "" && intentSessionDir != strings.TrimSpace(activeSessionDir)

	configsByName := make(map[string]mcp.ServerConfig, len(ac.MCPConfigs))
	for _, cfg := range ac.MCPConfigs {
		configsByName[cfg.Name] = cfg
	}
	var targets []string
	if len(req.Servers) == 0 {
		for _, cfg := range ac.MCPConfigs {
			if cfg.Manual {
				targets = append(targets, cfg.Name)
			}
		}
	} else {
		targets = append([]string(nil), req.Servers...)
	}

	var errs []error
	// Only manual MCP servers can be controlled via /mcp.
	for _, name := range targets {
		cfg, ok := configsByName[name]
		if !ok {
			errs = append(errs, fmt.Errorf("unknown MCP server %q", name))
			continue
		}
		if !cfg.Manual {
			errs = append(errs, fmt.Errorf("MCP server %q is not manual; only manual MCP servers can be enabled/disabled", name))
		}
	}

	manualByName := make(map[string]struct{}, len(configsByName))
	for name, cfg := range configsByName {
		if cfg.Manual {
			manualByName[name] = struct{}{}
		}
	}
	intentNames, intentErr := mcpIntentAfterControl(intentSessionDir, req.Action, targets, manualByName)
	if req.Action != agent.MCPControlEnable && req.Action != agent.MCPControlDisable {
		errs = append(errs, fmt.Errorf("unknown MCP action %q", req.Action))
	}
	if intentErr != nil && staleSession {
		return agent.MCPControlResult{}, fmt.Errorf("load MCP session intent: %w", intentErr)
	}
	if staleSession {
		if intentErr == nil {
			if err := persistMCPEnabledIntent(intentSessionDir, intentNames); err != nil {
				return agent.MCPControlResult{}, fmt.Errorf("persist MCP enable intent: %w", err)
			}
		}
		// The control belongs to a session that is no longer active. Its done
		// event is fenced by the agent readiness generation; do not connect or
		// disconnect anything in the current session's shared runtime.
		return agent.MCPControlResult{}, errors.Join(errs...)
	}

	var enabledOK, disabledOK []string
	switch req.Action {
	case agent.MCPControlEnable:
		for _, name := range targets {
			cfg, ok := configsByName[name]
			if !ok || !cfg.Manual {
				// already captured above
				continue
			}
			// Record intent before connecting. A failed first connection remains
			// enabled-but-unavailable and can be retried without losing the user's
			// explicit choice. Already connected servers are not reconnected.
			ac.MCPCatalog.SetDesiredEnabled(name, true)
			if ac.MCPMgr.Client(name) == nil {
				if err := ac.MCPMgr.ConnectOne(ctx, cfg); err != nil {
					errs = append(errs, fmt.Errorf("enable MCP %q: %w", name, err))
					continue
				}
			}
			enabledOK = append(enabledOK, name)
		}
	case agent.MCPControlDisable:
		for _, name := range targets {
			cfg, ok := configsByName[name]
			if !ok || !cfg.Manual {
				continue
			}
			ac.MCPMgr.Disconnect(name)
			ac.MCPCatalog.SetDesiredEnabled(name, false)
			disabledOK = append(disabledOK, name)
		}
	default:
		// already captured above
	}

	persistedNames := ac.MCPCatalog.EnabledServerNames()
	if intentErr == nil {
		persistedNames = intentNames
	}
	if err := persistMCPEnabledIntent(intentSessionDir, persistedNames); err != nil {
		errs = append(errs, fmt.Errorf("MCP enable intent applied but not persisted: %w", err))
	}
	// A session switch may happen while ConnectOne/Disconnect is in flight.
	// The switch's restore waits on this mutex, so reconcile here before
	// releasing it; the later restore remains harmless.
	if ac.MainAgent != nil {
		if currentSessionDir := ac.MainAgent.SessionDir(); currentSessionDir != intentSessionDir {
			if err := restoreMCPIntentForSession(ctx, ac, currentSessionDir); err != nil {
				log.Warnf("MCP control crossed a session switch; active intent restore failed error=%v", err)
			}
		}
	}

	mcpTools, toolErr := ac.MCPCatalog.DiscoverAllTools(ctx)
	if toolErr != nil {
		errs = append(errs, toolErr)
	}
	block := mcp.ConnectedServersPromptBlock(ctx, ac.MCPMgr)
	return agent.MCPControlResult{Tools: mcpTools, PromptBlock: block, Enabled: enabledOK, Disabled: disabledOK}, errors.Join(errs...)
}

func mcpIntentAfterControl(sessionDir string, action agent.MCPControlAction, targets []string, manualByName map[string]struct{}) ([]string, error) {
	meta, err := recovery.LoadSessionMeta(sessionDir)
	if err != nil {
		return nil, err
	}
	var names []string
	if meta != nil {
		names = append(names, meta.MCPEnabledServers...)
	}
	intent := make(map[string]struct{}, len(names)+len(targets))
	for _, name := range recovery.NormalizeMCPEnabledServers(names) {
		if _, ok := manualByName[name]; ok {
			intent[name] = struct{}{}
		}
	}
	for _, name := range targets {
		if _, ok := manualByName[name]; !ok {
			continue
		}
		switch action {
		case agent.MCPControlEnable:
			intent[name] = struct{}{}
		case agent.MCPControlDisable:
			delete(intent, name)
		}
	}
	result := make([]string, 0, len(intent))
	for name := range intent {
		result = append(result, name)
	}
	return recovery.NormalizeMCPEnabledServers(result), nil
}

// persistMCPEnabledIntent writes the current manual-MCP enabled intent into the
// metadata of the session that issued the control request. The directory is
// captured on the agent event loop at dispatch time, so an in-flight control
// cannot race a session switch into writing the old session's intent into the
// new session's metadata. Desired-enabled servers remain persisted when a
// connection attempt fails so a later restore can retry them.
func persistMCPEnabledIntent(sessionDir string, names []string) error {
	if strings.TrimSpace(sessionDir) == "" {
		return fmt.Errorf("session directory unavailable")
	}
	return recovery.SetMCPEnabledServers(sessionDir, names)
}

// restoreMCPIntent reconciles connections with the active session's persisted
// manual-MCP enabled intent. It never clears a desired-enabled server on
// connect failure: enabled means "should try", not "connected".
func restoreMCPIntent(ctx context.Context, ac *AppContext) error {
	if ac == nil {
		return nil
	}
	sessionDir := ac.SessionDir
	if ac.MainAgent != nil {
		sessionDir = ac.MainAgent.SessionDir()
	}
	return restoreMCPIntentForSession(ctx, ac, sessionDir)
}

func restoreMCPIntentForSession(ctx context.Context, ac *AppContext, sessionDir string) error {
	if ac == nil || ac.MCPMgr == nil || ac.MCPCatalog == nil {
		return nil
	}
	var persisted []string
	if sessionDir != "" {
		meta, err := recovery.LoadSessionMeta(sessionDir)
		if err != nil {
			return fmt.Errorf("load MCP session intent: %w", err)
		}
		if meta != nil {
			persisted = meta.MCPEnabledServers
		}
	}
	manualBy := make(map[string]mcp.ServerConfig, len(ac.MCPConfigs))
	for _, cfg := range ac.MCPConfigs {
		if cfg.Manual {
			manualBy[cfg.Name] = cfg
		}
	}
	want := make(map[string]struct{}, len(persisted))
	for _, name := range persisted {
		if _, ok := manualBy[name]; !ok {
			log.Debugf("MCP enabled server %q not present in config; ignoring restored intent", name)
			continue
		}
		want[name] = struct{}{}
	}

	// Disconnect manual servers that are currently connected but no longer wanted.
	for _, cfg := range ac.MCPConfigs {
		if !cfg.Manual {
			continue
		}
		if _, ok := want[cfg.Name]; ok {
			continue
		}
		ac.MCPCatalog.SetDesiredEnabled(cfg.Name, false)
		ac.MCPMgr.Disconnect(cfg.Name)
	}

	// Connect wanted manual servers that are not connected yet, in stable order.
	wantedNames := make([]string, 0, len(want))
	for name := range want {
		wantedNames = append(wantedNames, name)
	}
	sort.Strings(wantedNames)
	for _, name := range wantedNames {
		ac.MCPCatalog.SetDesiredEnabled(name, true)
		if ac.MCPMgr.Client(name) != nil {
			continue
		}
		if err := ac.MCPMgr.ConnectOne(ctx, manualBy[name]); err != nil {
			log.Warnf("MCP enabled server %q connect failed error=%v", name, err)
			// Keep desired intent; the endpoint stays pending/unavailable for retry.
		}
	}
	return nil
}

func loadSynchronousMCPState(ctx context.Context, ac *AppContext) (agent.MCPControlResult, error) {
	if err := restoreMCPIntent(ctx, ac); err != nil {
		return agent.MCPControlResult{}, err
	}
	return loadMCPState(ctx, ac.MCPCatalog, ac.MCPMgr)
}

// restoreMCPSessionIntentAsync reconciles MCP connections with the target
// session's persisted manual intent after a runtime session switch (/resume,
// /new, fork). It runs behind the agent's MCP readiness barrier so the first
// request of the new session cannot race catalog preparation.
func restoreMCPSessionIntentAsync(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil || ac.MCPMgr == nil {
		return
	}
	if ac.MCPCatalog == nil {
		ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)
	}
	sessionDir := ac.MainAgent.SessionDir()
	readyGeneration := ac.MainAgent.ResetMCPReady()
	restoreGeneration, restoreCtx := beginMCPRestore(ac)
	ac.MainAgent.NotifyEnvStatusUpdated()
	go func() {
		defer finishMCPRestore(ac, restoreGeneration)
		ac.mcpRestoreRunMu.Lock()
		defer ac.mcpRestoreRunMu.Unlock()
		if ac.mcpRestoreGen.Load() != restoreGeneration {
			ac.MainAgent.SetRuntimeMCPDiscoveryForGeneration(readyGeneration, nil, "")
			return
		}
		if err := restoreMCPIntentForSession(restoreCtx, ac, sessionDir); err != nil {
			log.Warnf("MCP intent restore failed error=%v", err)
		}
		if ac.mcpRestoreGen.Load() != restoreGeneration {
			ac.MainAgent.SetRuntimeMCPDiscoveryForGeneration(readyGeneration, nil, "")
			return
		}
		result, err := loadMCPState(restoreCtx, ac.MCPCatalog, ac.MCPMgr)
		if err != nil {
			log.Warnf("MCP tool discovery failed error=%v", err)
		}
		ac.MainAgent.SetRuntimeMCPDiscoveryForGeneration(readyGeneration, result.Tools, result.PromptBlock)
		ac.MainAgent.NotifyEnvStatusUpdated()
	}()
}

func beginMCPRestore(ac *AppContext) (uint64, context.Context) {
	parent := ac.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ac.mcpRestoreStateMu.Lock()
	defer ac.mcpRestoreStateMu.Unlock()
	generation := ac.mcpRestoreGen.Add(1)
	if ac.mcpRestoreCancel != nil {
		ac.mcpRestoreCancel()
	}
	ctx, cancel := context.WithCancel(parent)
	ac.mcpRestoreCancel = cancel
	return generation, ctx
}

func finishMCPRestore(ac *AppContext, generation uint64) {
	ac.mcpRestoreStateMu.Lock()
	defer ac.mcpRestoreStateMu.Unlock()
	if ac.mcpRestoreGen.Load() == generation {
		if ac.mcpRestoreCancel != nil {
			ac.mcpRestoreCancel()
		}
		ac.mcpRestoreCancel = nil
	}
}

func startRuntimeMCP(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil || ac.Registry == nil || ac.MCPMgr == nil || len(ac.MCPConfigs) == 0 {
		return
	}
	ac.mcpStartOnce.Do(func() {
		ac.mcpRuntimeStarted.Store(true)
		if ac.MCPCatalog == nil {
			ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)
		}
		// Register sentinels from configuration before connecting so SubAgents
		// never race the main runtime to initialize the same server.
		var mainServerNames []string
		for _, cfg := range ac.MCPConfigs {
			mainServerNames = append(mainServerNames, cfg.Name)
		}
		ac.MainAgent.RegisterMainMCPServers(mainServerNames)

		// Block initial requests until MCP has either connected or failed.
		readyGeneration := ac.MainAgent.ResetMCPReady()
		restoreGeneration, restoreCtx := beginMCPRestore(ac)
		sessionDir := ac.MainAgent.SessionDir()
		ac.MainAgent.NotifyEnvStatusUpdated()

		go func() {
			defer finishMCPRestore(ac, restoreGeneration)
			ac.mcpRestoreRunMu.Lock()
			defer ac.mcpRestoreRunMu.Unlock()
			if ac.mcpRestoreGen.Load() != restoreGeneration {
				ac.MainAgent.SetRuntimeMCPDiscoveryForGeneration(readyGeneration, nil, "")
				return
			}
			ac.MCPMgr.ConnectAll(restoreCtx, ac.MCPConfigs)
			if err := restoreMCPIntentForSession(restoreCtx, ac, sessionDir); err != nil {
				log.Warnf("MCP intent restore failed error=%v", err)
			}
			if ac.mcpRestoreGen.Load() != restoreGeneration {
				ac.MainAgent.SetRuntimeMCPDiscoveryForGeneration(readyGeneration, nil, "")
				return
			}
			result, err := loadMCPState(restoreCtx, ac.MCPCatalog, ac.MCPMgr)
			if err != nil {
				log.Warnf("MCP tool discovery failed error=%v", err)
			}
			ac.MainAgent.SetRuntimeMCPDiscoveryForGeneration(readyGeneration, result.Tools, result.PromptBlock)
			ac.MainAgent.NotifyEnvStatusUpdated()
		}()
	})
}

func startRuntimeWarmups(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil {
		return
	}
	go func() {
		if err := ac.MainAgent.PrewarmModelPolicy(); err != nil {
			log.Warnf("main-agent model policy prewarm failed error=%v", err)
		}
	}()
	go func() {
		if ac.MainAgent.ReloadAgentsMD() {
			log.Debug("project AGENTS.md loaded asynchronously")
		}
	}()
}
