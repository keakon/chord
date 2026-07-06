// Package main provides shared initialization logic between TUI mode (runRoot)
// and headless server mode (runServe).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/buildinfo"
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
	Ctx              context.Context
	Cancel           context.CancelFunc
	ProjectRoot      string
	ChordDir         string
	ConfigHome       string
	PathLocator      *config.PathLocator
	ProjectLocator   *config.ProjectLocator
	SessionDir       string
	Cfg              *config.Config
	GlobalCfg        *config.Config
	ProjectCfg       *config.Config
	Auth             config.AuthConfig
	LLMClient        *llm.Client
	ProviderName     string
	ModelID          string
	ProviderCfg      *llm.ProviderConfig // shared, safe for per-session reuse
	LLMProvider      llm.Provider        // shared HTTP transport
	ModelCfg         config.ModelConfig  // resolved model limits
	ProviderCache    *providerCache      // per-provider config cache (key cooldown shared across sessions)
	CtxMgr           *ctxmgr.Manager
	Registry         *tools.Registry
	HookEngine       hook.Manager
	LSPManager       *lsp.Manager
	MCPMgr           *mcp.Manager
	MCPConfigs       []mcp.ServerConfig
	RuntimeResources *runtimeResourceController
	MainAgent        *agent.MainAgent
	LoadedSkills     []*skill.Meta
	LoadedCommands   []*command.Definition
	LogWriter        *rotatingLogFile
	StderrRedirect   *stderrRedirect
	logCtx           logContext
	logLevel         golog.Level
	mcpStartOnce     sync.Once
	skillsLoadOnce   sync.Once
	SessionLock      *recovery.SessionLock
	InstanceID       string
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

type initAppStartupPlan struct {
	ProjectRoot       string
	ChordDir          string
	PathLocator       *config.PathLocator
	ProjectLocator    *config.ProjectLocator
	ConfigHome        string
	GlobalConfig      *config.Config
	ProjectConfig     *config.Config
	Config            *config.Config
	ProjectConfigPath string
}

func planInitAppStartup(projectRoot string) (*initAppStartupPlan, error) {
	if strings.TrimSpace(projectRoot) == "" {
		return nil, fmt.Errorf("project root is empty")
	}
	chordDir := filepath.Join(projectRoot, ".chord")
	if err := os.MkdirAll(chordDir, 0o700); err != nil {
		return nil, fmt.Errorf("create .chord directory: %w", err)
	}
	globalCfg, err := config.LoadConfig()
	if err != nil {
		return nil, wrapConfigLoadError("load config", err)
	}
	projectConfigPath := config.ProjectConfigPath(projectRoot)
	projectCfg, cfg, err := config.MergeProjectConfig(globalCfg, projectConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	pathLocator, err := config.ResolvePathLocator(globalCfg, config.PathOptions{})
	if err != nil {
		return nil, fmt.Errorf("resolve storage paths: %w", err)
	}
	projectLocator, err := pathLocator.EnsureProject(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project storage paths: %w", err)
	}
	return &initAppStartupPlan{
		ProjectRoot:       projectRoot,
		ChordDir:          chordDir,
		PathLocator:       pathLocator,
		ProjectLocator:    projectLocator,
		ConfigHome:        pathLocator.ConfigHome,
		GlobalConfig:      globalCfg,
		ProjectConfig:     projectCfg,
		Config:            cfg,
		ProjectConfigPath: projectConfigPath,
	}, nil
}

func applyInitAppStartupPlan(ac *AppContext, plan *initAppStartupPlan) {
	if ac == nil || plan == nil {
		return
	}
	ac.ProjectRoot = plan.ProjectRoot
	ac.ChordDir = plan.ChordDir
	ac.PathLocator = plan.PathLocator
	ac.ProjectLocator = plan.ProjectLocator
	ac.ConfigHome = plan.ConfigHome
	ac.GlobalCfg = plan.GlobalConfig
	ac.ProjectCfg = plan.ProjectConfig
	ac.Cfg = plan.Config
}

type initialLLMSetup struct {
	ProviderName   string
	ModelID        string
	InitialVariant string
	ModelCfg       config.ModelConfig
	ProviderCfg    *llm.ProviderConfig
	Provider       llm.Provider
	Client         *llm.Client
}

func resolveInitialModelSelection(agentConfigs map[string]*config.AgentConfig, poolPolicy *agent.RuntimeModelPoolPolicy) (string, string) {
	if agentConfigs == nil {
		return "", ""
	}
	builderCfg, ok := agentConfigs["builder"]
	if !ok || len(builderCfg.Models) == 0 {
		return "", ""
	}
	if resolvedRef := poolPolicy.ResolveInitialModelRef("builder", builderCfg); resolvedRef != "" {
		return config.ParseModelRef(resolvedRef)
	}
	if poolNames := builderCfg.PoolNames(); len(poolNames) > 0 {
		if firstPoolRefs := builderCfg.PoolModels(poolNames[0]); len(firstPoolRefs) > 0 {
			providerModel, variant := config.ParseModelRef(firstPoolRefs[0])
			if variant == "" {
				variant = builderCfg.Variant
			}
			return providerModel, variant
		}
	}
	return "", ""
}

func configureInitialClientModelPool(
	ac *AppContext,
	client *llm.Client,
	cfg *config.Config,
	auth config.AuthConfig,
	agentConfigs map[string]*config.AgentConfig,
	poolPolicy *agent.RuntimeModelPoolPolicy,
	defaultProviderModel string,
) {
	if ac == nil || client == nil || cfg == nil || agentConfigs == nil {
		return
	}
	builderCfg, ok := agentConfigs["builder"]
	if !ok || len(builderCfg.Models) == 0 {
		return
	}
	var poolModels []string
	if poolPolicy != nil {
		poolModels = poolPolicy.EffectiveModels("builder", builderCfg)
	}
	if len(poolModels) == 0 {
		if poolNames := builderCfg.PoolNames(); len(poolNames) > 0 {
			poolModels = builderCfg.PoolModels(poolNames[0])
		}
	}
	pool, selectedIdx := buildModelPool(
		ac.Ctx,
		poolModels,
		builderCfg.Variant,
		defaultProviderModel,
		cfg.Providers,
		auth,
		cfg.Proxy,
		cfg.MaxOutputTokens,
		ac.GetOrCreateProvider,
		ac.GetOrCreateProviderImpl,
		"builder startup",
	)
	if len(pool) > 1 {
		client.SetModelPool(pool, selectedIdx)
		log.Debugf("initial LLM client configured with builder model pool size=%v selected_idx=%v", len(pool), selectedIdx)
	}
}

func setupInitialLLMClient(
	ac *AppContext,
	cfg *config.Config,
	auth config.AuthConfig,
	authPath string,
	agentConfigs map[string]*config.AgentConfig,
	poolPolicy *agent.RuntimeModelPoolPolicy,
	defaultProviderModel, defaultVariant string,
) (*initialLLMSetup, error) {
	if ac == nil || cfg == nil {
		return nil, fmt.Errorf("missing app config for initial LLM setup")
	}
	providerName := ""
	if defaultProviderModel != "" {
		parts := strings.SplitN(defaultProviderModel, "/", 2)
		if len(parts) == 2 {
			providerName = parts[0]
		}
	}
	if providerName == "" {
		return nil, fmt.Errorf("no initial provider resolved from builder agent config")
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
		return nil, fmt.Errorf("no initial model ID resolved from builder agent config")
	}
	cfgProvider, modelCfg, err := config.LookupConfiguredModel(cfg.Providers, providerName, modelID)
	if err != nil {
		return nil, err
	}

	ac.ProviderCache = &providerCache{
		m:        make(map[string]*llm.ProviderConfig),
		impls:    make(map[string]llm.Provider),
		ctx:      ac.Ctx,
		auth:     auth,
		authPath: authPath,
		cfg:      cfg,
	}
	cfgProvider = applyRuntimeAPIBaseOverride(cfgProvider)
	creds := auth[providerName]
	apiKeys := config.ExtractAPIKeys(creds)
	providerCfg, err := ac.GetOrCreateProvider(providerName, cfgProvider, apiKeys)
	if err != nil {
		return nil, err
	}
	if cfgProvider.RateLimit > 0 {
		providerCfg.SetRateLimiter(cfgProvider.RateLimit)
	}

	effectiveProxy := llm.ResolveEffectiveProxy(cfgProvider.Proxy, cfg.Proxy)
	logEffectiveProxy(effectiveProxy)
	llmProvider, err := ac.GetOrCreateProviderImpl(providerName, cfgProvider, providerCfg, modelID)
	if err != nil {
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
		"",
	)
	llmClient.SetOutputTokenMax(cfg.MaxOutputTokens)
	llmClient.SetStreamRetryRounds(cfg.StreamRetryRounds)
	if initialVariant != "" {
		llmClient.SetVariant(initialVariant)
	}
	configureInitialClientModelPool(ac, llmClient, cfg, auth, agentConfigs, poolPolicy, defaultProviderModel)
	return &initialLLMSetup{
		ProviderName:   providerName,
		ModelID:        modelID,
		InitialVariant: initialVariant,
		ModelCfg:       modelCfg,
		ProviderCfg:    providerCfg,
		Provider:       llmProvider,
		Client:         llmClient,
	}, nil
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
	startupPlan, err := planInitAppStartup(projectRoot)
	if err != nil {
		ac.Cancel()
		return nil, err
	}
	applyInitAppStartupPlan(ac, startupPlan)
	globalCfg := ac.GlobalCfg
	projectCfg := ac.ProjectCfg
	cfg := ac.Cfg
	pathLocator := ac.PathLocator
	projectLocator := ac.ProjectLocator
	projectConfigPath := startupPlan.ProjectConfigPath

	if globalCfg.Maintenance.SizeCheckOnStartup {
		go func() {
			st, err := maintenance.BuildStatus(pathLocator)
			if err != nil {
				log.Warnf("maintenance size check failed error=%v", err)
				return
			}
			if globalCfg.Maintenance.WarnStateBytes > 0 && st.StateBytes >= globalCfg.Maintenance.WarnStateBytes {
				log.Warnf("Chord state directory is large path=%v bytes=%v", st.StateDir, st.StateBytes)
			}
			if globalCfg.Maintenance.WarnCacheBytes > 0 && st.CacheBytes >= globalCfg.Maintenance.WarnCacheBytes {
				log.Warnf("Chord cache directory is large path=%v bytes=%v", st.CacheDir, st.CacheBytes)
			}
		}()
	}

	logLevel := resolveLogLevel(globalCfg, projectCfg)
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

	log.Infof("chord starting %s", buildinfo.Current().LogString())
	if projectCfg != nil {
		log.Info("loaded project config")
		log.Debugf("loaded project config path=%v", projectConfigPath)
	}

	// Resolve agent configs once and reuse the result for both default-model
	// selection and MainAgent setup.
	agentConfigs, agentConfigsErr := config.ResolveAgentConfigs(
		filepath.Join(projectRoot, ".chord", "agents"),
		filepath.Join(pathLocator.ConfigHome, "agents"),
	)

	if agentConfigsErr == nil && agentConfigs != nil {
		if err := config.ResolveAgentModelPools(agentConfigs, cfg.ModelPools); err != nil {
			agentConfigsErr = err
		}
	}
	if agentConfigsErr != nil {
		ac.cleanup()
		return nil, fmt.Errorf("load agent configs: %w", agentConfigsErr)
	}

	// Load per-project model pool state early so the initial LLM client
	// can be constructed with the pool-selected model, avoiding an immediate
	// swap after startup.
	poolStatePath := config.ModelPoolStatePath(projectLocator.ProjectKey, pathLocator.StateDir)
	poolState, poolStateErr := config.LoadModelPoolState(poolStatePath)
	if poolStateErr != nil {
		log.Warnf("failed to load model pool state, using defaults error=%v", poolStateErr)
		poolState = &config.ModelPoolState{}
	}
	poolPolicy := agent.NewRuntimeModelPoolPolicy()
	if poolState.CurrentModelPool != "" {
		poolPolicy.SetCurrentModelPool(poolState.CurrentModelPool)
	}
	for agentName, poolName := range poolState.AgentOverrides {
		poolPolicy.SetAgentOverride(agentName, poolName)
	}

	// Determine the initial builder model before constructing the first shared LLM client.
	defaultProviderModel, defaultVariant := resolveInitialModelSelection(agentConfigs, poolPolicy)
	if defaultProviderModel == "" {
		ac.cleanup()
		return nil, fmt.Errorf("no initial model resolved from builder agent config: ensure builder agent defines at least one non-empty model pool")
	}

	authPath := filepath.Join(pathLocator.ConfigHome, "auth.yaml")
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		ac.cleanup()
		return nil, fmt.Errorf("load auth config: %w", err)
	}
	ac.Auth = auth

	initialLLM, err := setupInitialLLMClient(ac, cfg, auth, authPath, agentConfigs, poolPolicy, defaultProviderModel, defaultVariant)
	if err != nil {
		ac.cleanup()
		return nil, err
	}
	ac.ProviderName = initialLLM.ProviderName
	ac.ModelID = initialLLM.ModelID
	ac.LLMClient = initialLLM.Client
	ac.ProviderCfg = initialLLM.ProviderCfg
	ac.LLMProvider = initialLLM.Provider
	ac.ModelCfg = initialLLM.ModelCfg
	llmClient := initialLLM.Client
	providerCfg := initialLLM.ProviderCfg
	llmProvider := initialLLM.Provider
	modelID := initialLLM.ModelID
	modelCfg := initialLLM.ModelCfg
	initialVariant := initialLLM.InitialVariant

	log.Infof("configuration loaded model=%v max_output_tokens=%v context_window=%v", initialLLM.ModelID, initialLLM.ModelCfg.Limit.Output, initialLLM.ModelCfg.Limit.Context)
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

	tracePath := filepath.Join(ac.SessionDir, "traces", llm.LLMTraceFileName())
	var traceWriter *llm.TraceWriter
	if ac.ProviderCache.traceWriter != nil {
		traceWriter = ac.ProviderCache.traceWriter
		traceWriter.SetPath(tracePath)
	} else {
		traceWriter = llm.NewTraceWriter(tracePath)
	}
	ac.ProviderCache.setTraceWriter(traceWriter)

	// Enable LLM dump when effective log_level is "debug".
	if debugLoggingEnabled(ac.Cfg, ac.ProjectCfg) {
		dumpDir := filepath.Join(ac.SessionDir, "dumps", "llm")
		var dumpWriter *llm.DumpWriter
		if ac.ProviderCache.dumpWriter != nil {
			dumpWriter = ac.ProviderCache.dumpWriter
			dumpWriter.SetDir(dumpDir)
		} else {
			dumpWriter = llm.NewDumpWriter(dumpDir)
		}
		ac.ProviderCache.setDumpWriter(dumpWriter)
		log.Debugf("LLM dump enabled dir=%v", dumpDir)
	}

	// Context manager.
	ac.CtxMgr = ctxmgr.NewManagerWithInputBudget(
		modelCfg.Limit.Context,
		modelCfg.Limit.EffectiveInputBudget(cfg.MaxOutputTokens, llm.DefaultOutputTokenMax),
		cfg.Context.Compaction.Reserved,
		cfg.Context.Compaction.Threshold,
	)

	// Tool registry.
	ac.Registry = tools.NewRegistry()
	ac.Registry.Register(tools.ReadTool{BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.WriteTool{BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.PatchTool{BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.EditTool{BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.DeleteTool{BaseDir: ac.ProjectRoot})

	// Detect shell type and create appropriate ShellTool
	detectedShell, err := shell.DetectShell()
	if err != nil {
		log.Warnf("shell detection failed, using bash as default error=%v", err)
		detectedShell = shell.ShellBash
	}
	log.Debugf("detected shell for command execution shell=%v", detectedShell.String())
	shellTool := tools.NewShellTool(detectedShell.String())
	shellTool.BaseDir = ac.ProjectRoot
	ac.Registry.Register(shellTool)

	spawnTool := tools.NewSpawnTool(detectedShell.String())
	spawnTool.BaseDir = ac.ProjectRoot
	ac.Registry.Register(spawnTool)
	ac.Registry.Register(tools.SpawnStatusTool{})
	ac.Registry.Register(tools.SpawnStopTool{})
	ac.Registry.Register(tools.GrepTool{BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.GlobTool{BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.HandoffTool{BaseDir: ac.ProjectRoot})
	ac.Registry.Register(tools.NewWebFetchTool(cfg.WebFetch, cfg.Proxy))

	// MCP servers.
	mcpConfigs := mcp.ServerConfigsFromConfig(cfg.MCP)
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
				ac.MCPConfigs = mcpConfigs
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
		cfg, nil,
		mcp.ClientInfo{Name: "chord", Version: Version},
	)
	llmClient.SetSessionID(filepath.Base(ac.SessionDir))
	ac.MainAgent.SetInitialYoloMode(flagYolo)
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
		if ac.ProviderCache != nil && ac.ProviderCache.traceWriter != nil {
			ac.ProviderCache.traceWriter.SetPath(filepath.Join(sessionDir, "traces", llm.LLMTraceFileName()))
			ac.ProviderCache.setTraceWriter(ac.ProviderCache.traceWriter)
		}
		if debugLoggingEnabled(ac.Cfg, ac.ProjectCfg) && ac.ProviderCache != nil && ac.ProviderCache.dumpWriter != nil {
			ac.ProviderCache.dumpWriter.SetDir(filepath.Join(sessionDir, "dumps", "llm"))
			ac.ProviderCache.setDumpWriter(ac.ProviderCache.dumpWriter)
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
	viewImageTool := tools.NewViewImageTool(ac.MainAgent)
	viewImageTool.BaseDir = ac.ProjectRoot
	ac.Registry.Register(viewImageTool)

	// LLM factory for SubAgents.
	ac.MainAgent.SetLLMFactory(buildSubAgentLLMFactory(ac, providerCfg, llmProvider, modelID, modelCfg, cfg, auth))

	// Agent definitions.
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

	// Model pool policy: the policy was already initialized before LLM client
	// construction so the initial client uses the pool-selected model. Install
	// it on MainAgent now for runtime pool switching.
	ac.MainAgent.SetModelPoolPolicy(poolPolicy, poolStatePath)

	// Warn if the persisted current model pool is not defined by any agent.
	if poolState.CurrentModelPool != "" {
		poolDefined := false
		for _, cfg := range agentConfigs {
			if cfg.HasPool(poolState.CurrentModelPool) {
				poolDefined = true
				break
			}
		}
		if !poolDefined {
			log.Warnf("model pool state current model pool %q not defined by any agent, falling back to first pool", poolState.CurrentModelPool)
		}
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

func newLSPShutdownContext() (context.Context, context.CancelFunc) {
	// Close cancels ac.Ctx before stopping subsystems, but LSP shutdown still
	// needs its own short grace period to send shutdown/exit before force-kill.
	return context.WithTimeout(context.Background(), lspShutdownGrace)
}

// Close performs graceful shutdown of all components. It should be called
// when the application is exiting (typically via defer after initApp).
func (ac *AppContext) Close() {
	log.Info("shutting down")
	ac.Cancel()

	if ac.LSPManager != nil {
		stopCtx, cancel := newLSPShutdownContext()
		ac.LSPManager.Stop(stopCtx)
		cancel()
	}
	if ac.RuntimeResources != nil {
		ac.RuntimeResources.Stop()
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
