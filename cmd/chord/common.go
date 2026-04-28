// Package main provides shared initialization logic between TUI mode (runRoot)
// and headless server mode (runServe).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/maintenance"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/shell"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

const (
	lspShutdownGrace  = 200 * time.Millisecond
	agentShutdownWait = 2 * time.Second
)

// AppContext holds the shared initialized components used by both local TUI
// mode and headless server mode. After initApp() returns successfully, the
// caller must call Close() to release resources (log file, MCP connections,
// agent).
type AppContext struct {
	Ctx            context.Context
	Cancel         context.CancelFunc
	ProjectRoot    string
	ChordDir       string
	ConfigHome     string
	PathLocator    *config.PathLocator
	ProjectLocator *config.ProjectLocator
	SessionDir     string
	Cfg            *config.Config
	ProjectCfg     *config.Config
	Auth           config.AuthConfig
	LLMClient      *llm.Client
	ProviderName   string
	ModelID        string
	ProviderCfg    *llm.ProviderConfig // shared, safe for per-session reuse
	LLMProvider    llm.Provider        // shared HTTP transport
	ModelCfg       config.ModelConfig  // resolved model limits
	ProviderCache  *providerCache      // per-provider config cache (key cooldown shared across sessions)
	CtxMgr         *ctxmgr.Manager
	Registry       *tools.Registry
	HookEngine     hook.Manager
	LSPManager     *lsp.Manager
	MCPMgr         *mcp.Manager
	MCPConfigs     []mcp.ServerConfig
	MainAgent      *agent.MainAgent
	LoadedSkills   []*skill.Meta
	LoadedCommands []*command.Definition
	LogWriter      *rotatingLogFile
	StderrRedirect *stderrRedirect
	mcpStartOnce   sync.Once
	skillsLoadOnce sync.Once
	SessionLock    *recovery.SessionLock
	InstanceID     string
}

// GetOrCreateProvider returns the cached ProviderConfig for provName, or creates
// and caches one using cfg and apiKeys. Safe for concurrent use.
func (ac *AppContext) GetOrCreateProvider(provName string, cfg config.ProviderConfig, apiKeys []string) (*llm.ProviderConfig, error) {
	return ac.ProviderCache.getOrCreate(provName, cfg, apiKeys)
}

// GetOrCreateProviderImpl returns the cached Provider implementation for
// provName, or creates one using the already-normalized ProviderConfig.
func (ac *AppContext) GetOrCreateProviderImpl(provName string, cfg config.ProviderConfig, providerCfg *llm.ProviderConfig, modelID string) (llm.Provider, error) {
	return ac.ProviderCache.getOrCreateImpl(provName, cfg, providerCfg, modelID)
}

// initApp performs the shared initialization sequence used by local TUI and
// headless control-plane entrypoints. It sets up: signal context, project root, logging, config,
// auth, LLM client, session directory, context manager, tool registry, MCP,
// hooks, agent, skills, and agent definitions. When asyncMCP is true, MCP
// endpoints are exposed to the UI immediately as pending and connected later in
// the background.
//
// The caller must call ac.Close() when done (typically via defer).
func initApp(asyncMCP bool, mode string, sessionOpts sessionStartupOptions) (*AppContext, error) {
	ac := &AppContext{}

	// ---------------------------------------------------------------
	// 1. Signal-driven context for graceful shutdown (§14.5)
	// ---------------------------------------------------------------
	ac.Ctx, ac.Cancel = signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	ac.InstanceID = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

	// ---------------------------------------------------------------
	// 2. Project root (current working directory)
	// ---------------------------------------------------------------
	projectRoot, err := os.Getwd()
	if err != nil {
		ac.Cancel()
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	ac.ProjectRoot = projectRoot

	// ---------------------------------------------------------------
	// 3. Runtime directory
	// ---------------------------------------------------------------
	ac.ChordDir = filepath.Join(projectRoot, ".chord")
	if err := os.MkdirAll(ac.ChordDir, 0o700); err != nil {
		ac.Cancel()
		return nil, fmt.Errorf("create .chord directory: %w", err)
	}

	// ---------------------------------------------------------------
	// 4. Configuration
	// ---------------------------------------------------------------
	cfg, err := config.LoadConfig()
	if err != nil {
		ac.Cancel()
		return nil, fmt.Errorf("load config: %w", err)
	}
	ac.Cfg = cfg

	// Resolve path policy once so later startup steps can reuse it.
	pathLocator, err := config.ResolvePathLocator(cfg, config.PathOptions{})
	if err != nil {
		ac.cleanup()
		return nil, fmt.Errorf("resolve storage paths: %w", err)
	}
	projectLocator, err := pathLocator.EnsureProject(projectRoot)
	if err != nil {
		ac.cleanup()
		return nil, fmt.Errorf("resolve project storage paths: %w", err)
	}
	ac.PathLocator = pathLocator
	ac.ProjectLocator = projectLocator
	ac.ConfigHome = pathLocator.ConfigHome

	if cfg.Maintenance.SizeCheckOnStartup {
		go func() {
			st, err := maintenance.BuildStatus(pathLocator)
			if err != nil {
				slog.Warn("maintenance size check failed", "error", err)
				return
			}
			if cfg.Maintenance.WarnStateBytes > 0 && st.StateBytes >= cfg.Maintenance.WarnStateBytes {
				slog.Warn("Chord state directory is large", "path", st.StateDir, "bytes", st.StateBytes)
			}
			if cfg.Maintenance.WarnCacheBytes > 0 && st.CacheBytes >= cfg.Maintenance.WarnCacheBytes {
				slog.Warn("Chord cache directory is large", "path", st.CacheDir, "bytes", st.CacheBytes)
			}
		}()
	}

	logLevel := resolveLogLevel(cfg, nil)

	logPath := filepath.Join(pathLocator.LogsDir, "chord.log")
	logWriter, logErr := newRotatingLogFile(logPath)
	if logErr == nil {
		ac.LogWriter = logWriter
		handler := slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: logLevel})
		logger := slog.New(handler).With(
			"project_root", projectRoot,
			"pid", os.Getpid(),
			"instance_id", ac.InstanceID,
			"mode", mode,
		)
		if redirect, redirectErr := redirectProcessStderr(logWriter.CurrentFile(), logger); redirectErr != nil {
			writeStartupStderrNotice(logPath, redirectErr)
		} else {
			ac.StderrRedirect = redirect
			logWriter.SetStderrRedirect(redirect)
		}
		slog.SetDefault(logger)
	} else {
		fallbackLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})).With(
			"project_root", projectRoot,
			"pid", os.Getpid(),
			"instance_id", ac.InstanceID,
			"mode", mode,
		)
		slog.SetDefault(fallbackLogger)
		slog.Warn("runtime log unavailable; using stderr", "path", logPath, "error", logErr)
	}

	slog.Info("chord starting")

	// Try to load project-level config (.chord/config.yaml).
	projectConfigPath := filepath.Join(projectRoot, ".chord", "config.yaml")
	if pc, err := config.LoadConfigFromPath(projectConfigPath); err == nil {
		applyProjectConfigOverrides(ac, pc)
		slog.Info("loaded project config", "path", projectConfigPath)
	}

	if ac.ProjectCfg != nil && ac.ProjectCfg.LogLevel != "" {
		logLevel = resolveLogLevel(ac.Cfg, ac.ProjectCfg)
		if ac.LogWriter != nil {
			handler := slog.NewTextHandler(ac.LogWriter, &slog.HandlerOptions{Level: logLevel})
			slog.SetDefault(slog.New(handler).With(
				"project_root", projectRoot,
				"pid", os.Getpid(),
				"instance_id", ac.InstanceID,
				"mode", mode,
			))
		} else {
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})).With(
				"project_root", projectRoot,
				"pid", os.Getpid(),
				"instance_id", ac.InstanceID,
				"mode", mode,
			))
		}
	}

	// Resolve agent configs once and reuse the result for both default-model
	// selection and MainAgent setup.
	agentConfigs, agentConfigsErr := config.ResolveAgentConfigs(
		filepath.Join(projectRoot, ".chord", "agents"),
		filepath.Join(pathLocator.ConfigHome, "agents"),
	)

	// Determine active provider name and default model.
	// Priority: builder agent models[0] > alphabetical fallback.
	var defaultProviderModel string
	var defaultVariant string
	if agentConfigs != nil {
		if builderCfg, ok := agentConfigs["builder"]; ok && len(builderCfg.Models) > 0 {
			defaultProviderModel, defaultVariant = config.ParseModelRef(builderCfg.Models[0])
			if defaultVariant == "" {
				defaultVariant = builderCfg.Variant
			}
		}
	}

	ac.ProviderName = ""
	if ac.ProviderName == "" {
		if defaultProviderModel != "" {
			parts := strings.SplitN(defaultProviderModel, "/", 2)
			if len(parts) == 2 {
				ac.ProviderName = parts[0]
			}
		}
		if ac.ProviderName == "" {
			// No coder agent config: fall back to alphabetical order.
			names := make([]string, 0, len(cfg.Providers))
			for k := range cfg.Providers {
				names = append(names, k)
			}
			sort.Strings(names)
			if len(names) > 0 {
				ac.ProviderName = names[0]
			}
		}
	}

	// ---------------------------------------------------------------
	// 7. Auth — load API keys
	// ---------------------------------------------------------------
	authPath := filepath.Join(pathLocator.ConfigHome, "auth.yaml")

	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		ac.cleanup()
		return nil, fmt.Errorf("load auth config: %w", err)
	}

	ac.Auth = auth

	creds := auth[ac.ProviderName]
	apiKeys := config.ExtractAPIKeys(creds)

	// ---------------------------------------------------------------
	// 8. Resolve the active model from config.
	// ---------------------------------------------------------------
	cfgProvider, ok := cfg.Providers[ac.ProviderName]
	if !ok {
		ac.cleanup()
		return nil, fmt.Errorf("provider %q not found in config", ac.ProviderName)
	}

	modelID := ""
	initialVariant := ""
	if defaultProviderModel != "" {
		parts := strings.SplitN(defaultProviderModel, "/", 2)
		if len(parts) == 2 {
			modelID = parts[1]
			initialVariant = defaultVariant
		}
	}
	if modelID == "" {
		// No builder agent config: fall back to alphabetical order.
		modelNames := make([]string, 0, len(cfgProvider.Models))
		for k := range cfgProvider.Models {
			modelNames = append(modelNames, k)
		}
		sort.Strings(modelNames)
		if len(modelNames) > 0 {
			modelID = modelNames[0]
		}
	}
	modelCfg, ok := cfgProvider.Models[modelID]
	if !ok {
		ac.cleanup()
		return nil, fmt.Errorf("model %q not found in provider %q", modelID, ac.ProviderName)
	}
	ac.ModelID = modelID

	// ---------------------------------------------------------------
	// 10. LLM client (provider-based, streaming)
	// ---------------------------------------------------------------
	ac.ProviderCache = &providerCache{
		m:        make(map[string]*llm.ProviderConfig),
		impls:    make(map[string]llm.Provider),
		auth:     auth,
		authPath: authPath,
		cfg:      cfg,
	}
	if flagAPIBase != "" {
		cfgProvider.APIURL = flagAPIBase
	}
	providerCfg, err := ac.GetOrCreateProvider(ac.ProviderName, cfgProvider, apiKeys)
	if err != nil {
		ac.cleanup()
		return nil, err
	}
	if cfgProvider.RateLimit > 0 {
		providerCfg.SetRateLimiter(cfgProvider.RateLimit)
	}

	effectiveProxy := llm.ResolveEffectiveProxy(cfgProvider.Proxy, cfg.Proxy)
	logEffectiveProxy(effectiveProxy)
	if providerCfg.Type() == "google" {
		ac.cleanup()
		return nil, fmt.Errorf("google provider not yet implemented")
	}
	var llmProvider llm.Provider
	llmProvider, err = ac.GetOrCreateProviderImpl(ac.ProviderName, cfgProvider, providerCfg, modelID)
	if err != nil {
		ac.cleanup()
		return nil, err
	}
	if providerCfg.Type() == config.ProviderTypeResponses {
		slog.Info("using Responses API", "model", modelID, "api_url", providerCfg.APIURL())
	}

	llmClient := llm.NewClient(
		providerCfg,
		llmProvider,
		modelID,
		modelCfg.Limit.Output,
		"", // system prompt is set by agent.NewMainAgent
	)
	llmClient.SetOutputTokenMax(cfg.MaxOutputTokens)
	if initialVariant != "" {
		llmClient.SetVariant(initialVariant)
	}
	ac.LLMClient = llmClient
	ac.ProviderCfg = providerCfg
	ac.LLMProvider = llmProvider
	ac.ModelCfg = modelCfg

	slog.Info("configuration loaded",
		"model", modelID,
		"max_output_tokens", modelCfg.Limit.Output,
		"context_window", modelCfg.Limit.Context,
	)

	// ---------------------------------------------------------------
	// 11. Session directory (tool output truncation, logs, etc.)
	// ---------------------------------------------------------------
	slog.Debug("[DEBUG-BOOT] step 11: creating session directory")
	sessionPlan, err := planSessionStartup(projectLocator.ProjectSessionsDir, sessionOpts)
	if err != nil {
		ac.cleanup()
		return nil, err
	}
	ac.SessionDir = sessionPlan.SessionDir
	slog.Debug("[DEBUG-BOOT] step 11: session dir created", "session_dir", ac.SessionDir, "restore", sessionPlan.RestoreOnStartup)

	// Acquire exclusive cross-process ownership of the session directory.
	// This prevents two Chord processes from concurrently writing the same session.
	slog.Debug("[DEBUG-BOOT] step 11: acquiring session lock")
	if sessionLock, lockErr := recovery.AcquireSessionLock(ac.SessionDir); lockErr != nil {
		ac.cleanup()
		return nil, fmt.Errorf("acquire session lock: %w", lockErr)
	} else {
		ac.SessionLock = sessionLock
	}
	slog.Debug("[DEBUG-BOOT] step 11: session lock acquired")

	// Enable LLM dump when effective log_level is "debug".
	if debugLoggingEnabled(ac.Cfg, ac.ProjectCfg) {
		dumpDir := filepath.Join(ac.SessionDir, "dumps", "llm")
		var dumpWriter *llm.DumpWriter
		if ac.ProviderCache.dumpWriter != nil {
			dumpWriter = ac.ProviderCache.dumpWriter
			dumpWriter.SetDir(dumpDir)
		} else {
			dumpWriter = llm.NewDumpWriter(dumpDir)
			ac.ProviderCache.dumpWriter = dumpWriter
		}
		llm.SetProviderDumpWriter(llmProvider, dumpWriter)
		slog.Debug("LLM dump enabled", "dir", dumpDir)
	}

	// ---------------------------------------------------------------
	// 12. Context manager (conversation history + compression)
	// ---------------------------------------------------------------
	slog.Debug("[DEBUG-BOOT] step 12: creating context manager")
	ac.CtxMgr = ctxmgr.NewManager(
		modelCfg.Limit.Context,
		cfg.Context.AutoCompact,
		cfg.Context.CompactThreshold,
	)
	slog.Debug("[DEBUG-BOOT] step 12: context manager created")

	// ---------------------------------------------------------------
	// 13. Tool registry — register the basic tools.
	// ---------------------------------------------------------------
	slog.Debug("[DEBUG-BOOT] step 13: registering tools")
	ac.Registry = tools.NewRegistry()
	ac.Registry.Register(tools.ReadTool{})
	ac.Registry.Register(tools.WriteTool{})
	ac.Registry.Register(tools.EditTool{})
	ac.Registry.Register(tools.DeleteTool{})

	// Detect shell type and create appropriate BashTool
	detectedShell, err := shell.DetectShell()
	if err != nil {
		slog.Warn("shell detection failed, using bash as default", "error", err)
		detectedShell = shell.ShellBash
	}
	slog.Info("detected shell for command execution", "shell", detectedShell.String())
	ac.Registry.Register(tools.NewBashTool(detectedShell.String()))

	ac.Registry.Register(tools.NewSpawnTool(detectedShell.String()))
	ac.Registry.Register(tools.SpawnStatusTool{})
	ac.Registry.Register(tools.SpawnStopTool{})
	ac.Registry.Register(tools.GrepTool{})
	ac.Registry.Register(tools.GlobTool{})
	ac.Registry.Register(tools.HandoffTool{})
	ac.Registry.Register(tools.NewWebFetchTool(cfg.WebFetch, cfg.Proxy))

	// ---------------------------------------------------------------
	// 13a. MCP servers — discover and register tools.
	// ---------------------------------------------------------------
	mcpConfigs := mcp.ServerConfigsFromConfig(cfg.MCP)
	slog.Debug("[DEBUG-BOOT] step 13a: MCP servers", "config_count", len(mcpConfigs))
	if ac.ProjectCfg != nil {
		mcpConfigs = append(mcpConfigs, mcp.ServerConfigsFromConfig(ac.ProjectCfg.MCP)...)
	}
	syncMCPPromptBlock := ""
	if len(mcpConfigs) > 0 {
		if asyncMCP {
			ac.MCPConfigs = mcpConfigs
			ac.MCPMgr = mcp.NewPendingManager(mcpConfigs)
		} else {
			mgr, err := mcp.NewManager(ac.Ctx, mcpConfigs)
			if err != nil {
				slog.Warn("MCP initialization failed", "error", err)
			} else {
				ac.MCPMgr = mgr
				if len(mgr.Clients()) > 0 {
					mcpTools, err := mcp.DiscoverAllTools(ac.Ctx, mgr)
					if err != nil {
						slog.Warn("MCP tool discovery failed", "error", err)
					} else {
						for _, t := range mcpTools {
							ac.Registry.Register(t)
							slog.Info("registered MCP tool", "name", t.Name())
						}
					}
					syncMCPPromptBlock = mcp.ConnectedServersPromptBlock(ac.Ctx, mgr)
				}
			}
		}
	}

	// ---------------------------------------------------------------
	// 14. Hook engine.
	// ---------------------------------------------------------------
	slog.Debug("[DEBUG-BOOT] step 14: hook engine")
	hookDefs := hookDefsFromConfig(cfg.Hooks)
	if ac.ProjectCfg != nil {
		hookDefs = append(hookDefs, hookDefsFromConfig(ac.ProjectCfg.Hooks)...)
	}
	if len(hookDefs) > 0 {
		ac.HookEngine = hook.NewCommandEngineFromList(hookDefs)
		slog.Info("hook engine loaded", "hook_count", len(hookDefs))
	} else {
		ac.HookEngine = &hook.NoopEngine{}
	}

	// ---------------------------------------------------------------
	// 15. Agent (event loop + LLM interaction)
	// ---------------------------------------------------------------
	slog.Debug("[DEBUG-BOOT] step 15: creating MainAgent")
	ac.MainAgent = agent.NewMainAgent(
		ac.Ctx, llmClient, ac.CtxMgr, ac.Registry, ac.HookEngine,
		ac.SessionDir, modelID, projectRoot,
		cfg, ac.ProjectCfg,
	)
	llmClient.SetSessionID(filepath.Base(ac.SessionDir))
	slog.Debug("[DEBUG-BOOT] step 15: MainAgent created, setting up session lock and callbacks")
	ac.MainAgent.SetSessionLock(ac.SessionLock)
	ac.MainAgent.SetSessionArtifactsDirFunc(func() string {
		if ac == nil || strings.TrimSpace(ac.SessionDir) == "" {
			return ""
		}
		return filepath.Join(ac.SessionDir, "artifacts")
	})
	ac.MainAgent.SetSessionTargetChangedFunc(func(sessionDir string) {
		if ac == nil {
			return
		}
		sessionDir = strings.TrimSpace(sessionDir)
		if sessionDir == "" {
			return
		}
		ac.SessionDir = sessionDir
		if debugLoggingEnabled(ac.Cfg, ac.ProjectCfg) && ac.ProviderCache != nil && ac.ProviderCache.dumpWriter != nil {
			ac.ProviderCache.dumpWriter.SetDir(filepath.Join(sessionDir, "dumps", "llm"))
		}
		// Refresh skills metadata on session switch (non-blocking).
		refreshSkills(ac)
	})
	ac.SessionLock = nil
	ac.MainAgent.SetProviderModelRef(ac.ProviderName + "/" + modelID)
	if initialVariant != "" {
		ac.MainAgent.SetProviderModelRef(ac.ProviderName + "/" + modelID + "@" + initialVariant)
	}
	ensureRuntimeLSP(ac)
	configureRuntimeStateProviders(ac)
	slog.Debug("[DEBUG-BOOT] step 15: LSP and state providers configured")

	// ---------------------------------------------------------------
	// 15b. Phase 2a tools — require agent references.
	// ---------------------------------------------------------------
	// TodoWrite is always registered; Delegate is registered later (15d) after
	// agent configs are loaded, only when subagent-mode agents are available.
	ac.Registry.Register(tools.NewTodoWriteTool(ac.MainAgent))
	ac.Registry.Register(tools.NewSkillTool(ac.MainAgent))

	// ---------------------------------------------------------------
	// 15c. LLM factory for SubAgents.
	// ---------------------------------------------------------------
	ac.MainAgent.SetLLMFactory(buildSubAgentLLMFactory(ac, providerCfg, llmProvider, modelID, modelCfg, cfg, auth))

	// ---------------------------------------------------------------
	// 15d. Agent definitions.
	// ---------------------------------------------------------------
	slog.Debug("[DEBUG-BOOT] step 15d: agent definitions", "configs_err", agentConfigsErr, "configs_count", len(agentConfigs))
	if agentConfigsErr != nil {
		ac.cleanup()
		return nil, fmt.Errorf("load agent configs: %w", agentConfigsErr)
	}
	if len(agentConfigs) > 0 {
		names := make([]string, 0, len(agentConfigs))
		for name := range agentConfigs {
			names = append(names, name)
		}
		slog.Info("agent definitions resolved", "count", len(agentConfigs), "names", names)
	}
	if agentConfigs != nil {
		ac.MainAgent.SetAgentConfigs(agentConfigs)
	}
	// ---------------------------------------------------------------
	// 15e. Model switch factory.
	// ---------------------------------------------------------------
	ac.MainAgent.SetModelSwitchFactory(buildMainClientFactory(ac, cfg, auth))

	if sessionPlan.RestoreOnStartup {
		if err := ac.MainAgent.RestoreSessionAtStartup(); err != nil {
			ac.cleanup()
			return nil, fmt.Errorf("restore startup session: %w", err)
		}
	}

	// Register delegate-control tools only when at least one subagent-mode agent is available.
	if ac.MainAgent.HasAvailableSubAgents() {
		ac.Registry.Register(tools.NewDelegateTool(ac.MainAgent))
		ac.Registry.Register(tools.NewNotifyTool(nil, ac.MainAgent, false, true))
		ac.Registry.Register(tools.NewCancelTool(ac.MainAgent))
	}

	ac.MainAgent.SetAvailableModelsFn(buildAvailableModelsFn(ac, cfg, auth))

	// ---------------------------------------------------------------
	// 15a. Skill loading.
	// ---------------------------------------------------------------
	slog.Debug("[DEBUG-BOOT] step 15a: skill loading")
	startAsyncSkillLoad(ac)

	// ---------------------------------------------------------------
	// 15b. Custom command loading (synchronous — needed before first input).
	// ---------------------------------------------------------------
	slog.Debug("[DEBUG-BOOT] step 15b: custom command loading")
	loadCustomCommands(ac)

	applyInitialMCPPromptState(ac, asyncMCP, len(mcpConfigs) > 0, syncMCPPromptBlock)

	return ac, nil
}

// cleanup releases resources acquired during initApp when initialization
// fails partway through. For the full lifecycle, use Close() instead.
func (ac *AppContext) cleanup() {
	if ac.SessionLock != nil {
		if err := ac.SessionLock.Release(); err != nil {
			slog.Warn("session lock cleanup failed", "error", err)
		}
		ac.SessionLock = nil
	}
	if ac.Cancel != nil {
		ac.Cancel()
	}
	if ac.StderrRedirect != nil {
		_ = ac.StderrRedirect.Restore()
		ac.StderrRedirect = nil
	}
	if ac.LogWriter != nil {
		_ = ac.LogWriter.Close()
		ac.LogWriter = nil
	}
}

// Close performs graceful shutdown of all components. It should be called
// when the application is exiting (typically via defer after initApp).
func (ac *AppContext) Close() {
	slog.Info("shutting down")
	ac.Cancel()

	if ac.LSPManager != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), lspShutdownGrace)
		ac.LSPManager.Stop(stopCtx)
		cancel()
	}
	if ac.MCPMgr != nil {
		ac.MCPMgr.Close()
	}

	if ac.MainAgent != nil {
		if err := ac.MainAgent.Shutdown(agentShutdownWait); err != nil {
			slog.Warn("agent shutdown incomplete", "error", err)
		}
	}
	if ac.SessionLock != nil {
		if err := ac.SessionLock.Release(); err != nil {
			slog.Warn("session lock release failed", "error", err)
		}
	}
	if ac.ProviderCache != nil {
		ac.ProviderCache.close()
	}

	if ac.LogWriter != nil {
		_ = ac.LogWriter.Sync()
	}

	slog.Info("chord stopped")

	if ac.StderrRedirect != nil {
		_ = ac.StderrRedirect.Restore()
		ac.StderrRedirect = nil
	}
	if ac.LogWriter != nil {
		_ = ac.LogWriter.Close()
		ac.LogWriter = nil
	}
}
