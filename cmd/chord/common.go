// Package main provides shared initialization logic between TUI mode (runRoot)
// and headless server mode (runServe).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

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
	logCtx         logContext
	logLevel       golog.Level
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

	// Signal handling and process identity.
	ac.Ctx, ac.Cancel = signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	ac.InstanceID = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

	// Project root.
	projectRoot, err := os.Getwd()
	if err != nil {
		ac.Cancel()
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	ac.ProjectRoot = projectRoot

	// Runtime directory.
	ac.ChordDir = filepath.Join(projectRoot, ".chord")
	if err := os.MkdirAll(ac.ChordDir, 0o700); err != nil {
		ac.Cancel()
		return nil, fmt.Errorf("create .chord directory: %w", err)
	}

	// Configuration.
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
				log.Warnf("maintenance size check failed error=%v", err)
				return
			}
			if cfg.Maintenance.WarnStateBytes > 0 && st.StateBytes >= cfg.Maintenance.WarnStateBytes {
				log.Warnf("Chord state directory is large path=%v bytes=%v", st.StateDir, st.StateBytes)
			}
			if cfg.Maintenance.WarnCacheBytes > 0 && st.CacheBytes >= cfg.Maintenance.WarnCacheBytes {
				log.Warnf("Chord cache directory is large path=%v bytes=%v", st.CacheDir, st.CacheBytes)
			}
		}()
	}

	logLevel := resolveLogLevel(cfg, nil)
	ac.logLevel = logLevel
	logCtx := logContext{PWD: projectRoot, PID: os.Getpid()}
	ac.logCtx = logCtx

	logPath := filepath.Join(pathLocator.LogsDir, "chord.log")
	logWriter, logErr := newRotatingLogFile(logPath)
	if logErr == nil {
		ac.LogWriter = logWriter
		logger := newGologLoggerWithContext(logWriter, logLevel, logCtx)
		if redirect, redirectErr := redirectProcessStderr(logWriter.CurrentFile(), logger); redirectErr != nil {
			writeStartupStderrNotice(logPath, redirectErr)
		} else {
			ac.StderrRedirect = redirect
			logWriter.SetStderrRedirect(redirect)
		}
		setDefaultLogger(logger)
	} else {
		fallbackLogger := newStderrGologLoggerWithContext(logLevel, logCtx)
		setDefaultLogger(fallbackLogger)
		log.Warnf("runtime log unavailable; using stderr path=%v error=%v", logPath, logErr)
	}

	log.Info("chord starting")

	// Try to load project-level config (.chord/config.yaml).
	projectConfigPath := filepath.Join(projectRoot, ".chord", "config.yaml")
	if pc, err := config.LoadConfigFromPath(projectConfigPath); err == nil {
		applyProjectConfigOverrides(ac, pc)
		log.Debugf("loaded project config path=%v", projectConfigPath)
	}

	if ac.ProjectCfg != nil && ac.ProjectCfg.LogLevel != "" {
		ac.logLevel = resolveLogLevel(ac.Cfg, ac.ProjectCfg)
		if ac.LogWriter != nil {
			logger := newGologLoggerWithContext(ac.LogWriter, ac.logLevel, ac.logCtx)
			setDefaultLogger(logger)
			ac.StderrRedirect.SetLogger(logger)
		} else {
			setDefaultLogger(newStderrGologLoggerWithContext(ac.logLevel, ac.logCtx))
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

	// Auth and API keys.
	authPath := filepath.Join(pathLocator.ConfigHome, "auth.yaml")

	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		ac.cleanup()
		return nil, fmt.Errorf("load auth config: %w", err)
	}

	ac.Auth = auth

	creds := auth[ac.ProviderName]
	apiKeys := config.ExtractAPIKeys(creds)

	// Active model.
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

	// LLM client.
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
		log.Debugf("using Responses API model=%v api_url=%v", modelID, providerCfg.APIURL())
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

	log.Infof("configuration loaded model=%v max_output_tokens=%v context_window=%v", modelID, modelCfg.Limit.Output, modelCfg.Limit.Context)

	// Session directory.
	sessionPlan, err := planSessionStartup(projectLocator.ProjectSessionsDir, sessionOpts)
	if err != nil {
		ac.cleanup()
		return nil, err
	}
	ac.SessionDir = sessionPlan.SessionDir
	ac.logCtx.SID = filepath.Base(ac.SessionDir)
	if ac.LogWriter != nil {
		logger := newGologLoggerWithContext(ac.LogWriter, ac.logLevel, ac.logCtx)
		setDefaultLogger(logger)
		ac.StderrRedirect.SetLogger(logger)
	} else {
		setDefaultLogger(newStderrGologLoggerWithContext(ac.logLevel, ac.logCtx))
	}

	// Acquire exclusive cross-process ownership of the session directory.
	// This prevents two Chord processes from concurrently writing the same session.
	if sessionLock, lockErr := recovery.AcquireSessionLock(ac.SessionDir); lockErr != nil {
		ac.cleanup()
		return nil, fmt.Errorf("acquire session lock: %w", lockErr)
	} else {
		ac.SessionLock = sessionLock
	}

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
		log.Debugf("LLM dump enabled dir=%v", dumpDir)
	}

	// Context manager.
	ac.CtxMgr = ctxmgr.NewManager(
		modelCfg.Limit.Context,
		cfg.Context.AutoCompact,
		cfg.Context.CompactThreshold,
	)

	// Tool registry.
	ac.Registry = tools.NewRegistry()
	ac.Registry.Register(tools.ReadTool{})
	ac.Registry.Register(tools.WriteTool{})
	ac.Registry.Register(tools.EditTool{})
	ac.Registry.Register(tools.DeleteTool{})

	// Detect shell type and create appropriate BashTool
	detectedShell, err := shell.DetectShell()
	if err != nil {
		log.Warnf("shell detection failed, using bash as default error=%v", err)
		detectedShell = shell.ShellBash
	}
	log.Debugf("detected shell for command execution shell=%v", detectedShell.String())
	ac.Registry.Register(tools.NewBashTool(detectedShell.String()))

	ac.Registry.Register(tools.NewSpawnTool(detectedShell.String()))
	ac.Registry.Register(tools.SpawnStatusTool{})
	ac.Registry.Register(tools.SpawnStopTool{})
	ac.Registry.Register(tools.GrepTool{})
	ac.Registry.Register(tools.GlobTool{})
	ac.Registry.Register(tools.HandoffTool{})
	ac.Registry.Register(tools.NewWebFetchTool(cfg.WebFetch, cfg.Proxy))

	// MCP servers.
	mcpConfigs := mcp.ServerConfigsFromConfig(cfg.MCP)
	if ac.ProjectCfg != nil {
		mcpConfigs = append(mcpConfigs, mcp.ServerConfigsFromConfig(ac.ProjectCfg.MCP)...)
	}
	syncMCPPromptBlock := ""
	if len(mcpConfigs) > 0 {
		if asyncMCP {
			ac.MCPConfigs = mcpConfigs
			ac.MCPMgr = mcp.NewPendingManagerWithClientInfo(mcpConfigs, mcp.ClientInfo{Name: "chord", Version: Version})
		} else {
			mgr, err := mcp.NewManagerWithClientInfo(ac.Ctx, mcpConfigs, mcp.ClientInfo{Name: "chord", Version: Version})
			if err != nil {
				log.Warnf("MCP initialization failed error=%v", err)
			} else {
				ac.MCPMgr = mgr
				if len(mgr.Clients()) > 0 {
					mcpTools, err := mcp.DiscoverAllTools(ac.Ctx, mgr)
					if err != nil {
						log.Warnf("MCP tool discovery failed error=%v", err)
					} else {
						for _, t := range mcpTools {
							ac.Registry.Register(t)
							log.Debugf("registered MCP tool name=%v", t.Name())
						}
					}
					syncMCPPromptBlock = mcp.ConnectedServersPromptBlock(ac.Ctx, mgr)
				}
			}
		}
	}

	// Hook engine.
	hookDefs := hookDefsFromConfig(cfg.Hooks)
	if ac.ProjectCfg != nil {
		hookDefs = append(hookDefs, hookDefsFromConfig(ac.ProjectCfg.Hooks)...)
	}
	if len(hookDefs) > 0 {
		ac.HookEngine = hook.NewCommandEngineFromList(hookDefs)
		log.Debugf("hook engine loaded hook_count=%v", len(hookDefs))
	} else {
		ac.HookEngine = &hook.NoopEngine{}
	}

	// Main agent.
	ac.MainAgent = agent.NewMainAgent(
		ac.Ctx, llmClient, ac.CtxMgr, ac.Registry, ac.HookEngine,
		ac.SessionDir, modelID, projectRoot,
		cfg, ac.ProjectCfg,
		mcp.ClientInfo{Name: "chord", Version: Version},
	)
	llmClient.SetSessionID(filepath.Base(ac.SessionDir))
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
		ac.logCtx.SID = filepath.Base(sessionDir)
		if ac.LogWriter != nil {
			logger := newGologLoggerWithContext(ac.LogWriter, ac.logLevel, ac.logCtx)
			setDefaultLogger(logger)
			ac.StderrRedirect.SetLogger(logger)
		} else {
			setDefaultLogger(newStderrGologLoggerWithContext(ac.logLevel, ac.logCtx))
		}
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

	// Agent-bound tools.
	// TodoWrite is always registered; Delegate is registered later after agent
	// configs are loaded, only when subagent-mode agents are available.
	ac.Registry.Register(tools.NewTodoWriteTool(ac.MainAgent))
	ac.Registry.Register(tools.NewSkillTool(ac.MainAgent))

	// LLM factory for SubAgents.
	ac.MainAgent.SetLLMFactory(buildSubAgentLLMFactory(ac, providerCfg, llmProvider, modelID, modelCfg, cfg, auth))

	// Agent definitions.
	if agentConfigsErr != nil {
		ac.cleanup()
		return nil, fmt.Errorf("load agent configs: %w", agentConfigsErr)
	}
	if len(agentConfigs) > 0 {
		names := make([]string, 0, len(agentConfigs))
		for name := range agentConfigs {
			names = append(names, name)
		}
		log.Debugf("agent definitions resolved count=%v names=%v", len(agentConfigs), names)
	}
	if agentConfigs != nil {
		ac.MainAgent.SetAgentConfigs(agentConfigs)
	}
	// Model switch factory.
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

	// Skill loading.
	startAsyncSkillLoad(ac)

	// Custom command loading (synchronous — needed before first input).
	loadCustomCommands(ac)

	applyInitialMCPPromptState(ac, asyncMCP, len(mcpConfigs) > 0, syncMCPPromptBlock)

	return ac, nil
}

// cleanup releases resources acquired during initApp when initialization
// fails partway through. For the full lifecycle, use Close() instead.
func (ac *AppContext) cleanup() {
	if ac.SessionLock != nil {
		if err := ac.SessionLock.Release(); err != nil {
			log.Warnf("session lock cleanup failed error=%v", err)
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
	log.Info("shutting down")
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
			log.Warnf("agent shutdown incomplete error=%v", err)
		}
	}
	if ac.SessionLock != nil {
		if err := ac.SessionLock.Release(); err != nil {
			log.Warnf("session lock release failed error=%v", err)
		}
	}
	if ac.ProviderCache != nil {
		ac.ProviderCache.close()
	}

	if ac.LogWriter != nil {
		_ = ac.LogWriter.Sync()
	}

	log.Info("chord stopped")

	if ac.StderrRedirect != nil {
		_ = ac.StderrRedirect.Restore()
		ac.StderrRedirect = nil
	}
	if ac.LogWriter != nil {
		_ = ac.LogWriter.Close()
		ac.LogWriter = nil
	}
}
