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
	"sync/atomic"
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
	"github.com/keakon/chord/internal/worktree"
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
	Ctx    context.Context
	Cancel context.CancelFunc
	// ContentRoot is where project content and machine state are anchored:
	// project config, agent definitions, skills, memory, AGENTS.md, and the
	// session project key. It is the main worktree root when chord runs inside
	// a linked worktree, and the startup directory otherwise.
	ContentRoot string
	// WorkDir is the checkout this session works in. Tool base directories,
	// shell cwd, and the LSP root resolve against it.
	WorkDir          string
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
	MCPCatalog       *mcp.Catalog
	MCPConfigs       []mcp.ServerConfig
	RuntimeResources *runtimeResourceController
	MainAgent        *agent.MainAgent
	// ACPMode is set when this process serves the Agent Client Protocol.
	// Question has no client bridge there, so the tool fails instead of waiting.
	ACPMode           bool
	LoadedSkills      []*skill.Meta
	LoadedCommands    []*command.Definition
	LogWriter         *rotatingLogFile
	StderrRedirect    *stderrRedirect
	logCtx            logContext
	logLevel          golog.Level
	mcpStartOnce      sync.Once
	mcpRuntimeStarted atomic.Bool
	mcpRestoreRunMu   sync.Mutex
	mcpRestoreStateMu sync.Mutex
	mcpRestoreCancel  context.CancelFunc
	mcpRestoreGen     atomic.Uint64
	skillsLoadOnce    sync.Once
	// skillsRefreshGen orders asynchronous project-skill scans. A worktree
	// switch starts a scan that must not overwrite the catalog a later switch
	// already installed; see refreshSkillsFromDirs.
	skillsRefreshGen atomic.Uint64
	SessionLock      *recovery.SessionLock
	// StartupSkippedLockedSessions names the sessions --continue passed over
	// at startup because another live process owned them. Headless surfaces
	// them in the ready envelope; the TUI gets the same notice as a toast from
	// the MainAgent.
	StartupSkippedLockedSessions []string
	InstanceID                   string
}

// GetOrCreateProvider returns the cached ProviderConfig for provName, or creates
// and caches one using cfg and apiKeys. Safe for concurrent use.
func (ac *AppContext) GetOrCreateProvider(provName string, cfg config.ProviderConfig, apiKeys []string) (*llm.ProviderConfig, error) {
	return ac.ProviderCache.getOrCreate(provName, cfg, apiKeys)
}

// collectStartupConfigIssues re-runs the loader's strict checks over the
// global and project config files so startup can surface (as a one-time toast)
// the problems the tolerant loader logged and treated as not configured.
// Errors resolving or reading a file are ignored: they are already logged by
// the load path, and the toast should only report values that were dropped.
func collectStartupConfigIssues(plan *initAppStartupPlan) []string {
	if plan == nil {
		return nil
	}
	var issues []string
	if globalPath, err := config.ConfigPath(); err == nil {
		if list, err := config.CollectConfigFileIssues(globalPath, true); err == nil {
			issues = append(issues, list...)
		}
	}
	if plan.ProjectConfigPath != "" {
		if list, err := config.CollectProjectConfigIssues(plan.ProjectConfigPath); err == nil {
			issues = append(issues, list...)
		}
	}
	return issues
}

// GetOrCreateProviderImpl returns the cached Provider implementation for
// provName, or creates one using the already-normalized ProviderConfig.
func (ac *AppContext) GetOrCreateProviderImpl(provName string, cfg config.ProviderConfig, providerCfg *llm.ProviderConfig, modelID string) (llm.Provider, error) {
	return ac.ProviderCache.getOrCreateImpl(provName, cfg, providerCfg, modelID)
}

type initAppStartupPlan struct {
	ContentRoot       string
	WorkDir           string
	PathLocator       *config.PathLocator
	ProjectLocator    *config.ProjectLocator
	ConfigHome        string
	GlobalConfig      *config.Config
	ProjectConfig     *config.Config
	Config            *config.Config
	ProjectConfigPath string
}

// planInitAppStartup resolves everything startup needs before initApp wires the
// runtime. contentRoot anchors project content, control-plane configuration, and
// machine state; workDir is the checkout this session works in. Both must be
// canonical absolute paths.
func planInitAppStartup(contentRoot, workDir string) (*initAppStartupPlan, error) {
	if strings.TrimSpace(contentRoot) == "" {
		return nil, fmt.Errorf("content root is empty")
	}
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("work dir is empty")
	}
	if err := os.MkdirAll(filepath.Join(contentRoot, ".chord"), 0o700); err != nil {
		return nil, fmt.Errorf("create .chord directory: %w", err)
	}
	globalCfg, err := config.LoadConfig()
	if err != nil {
		return nil, wrapConfigLoadError("load config", err)
	}
	projectConfigPath := config.ProjectConfigPath(contentRoot)
	projectCfg, cfg, err := config.MergeProjectConfig(globalCfg, projectConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	pathLocator, err := config.ResolvePathLocator(globalCfg, config.PathOptions{})
	if err != nil {
		return nil, fmt.Errorf("resolve storage paths: %w", err)
	}
	projectLocator, err := pathLocator.EnsureProject(contentRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project storage paths: %w", err)
	}
	return &initAppStartupPlan{
		ContentRoot:       contentRoot,
		WorkDir:           workDir,
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
	ac.ContentRoot = plan.ContentRoot
	ac.WorkDir = plan.WorkDir
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

// chordLogFileName is the file inside the log directory that every entrypoint
// writes to; the ACP stdout guard redirects fd 1 into the same file.
const chordLogFileName = "chord.log"

// runtimeLogFileName is the file inside the log directory this process writes
// to. Only `chord acp` changes it: the mux frontend and each of its session
// children write their own file, because several processes that share one log
// also judge its size and rotate it, and concurrent renames overwrite each
// other.
var runtimeLogFileName = chordLogFileName

// initApp performs the shared initialization sequence used by local TUI and
// headless control-plane entrypoints. It sets up: signal context, project root, logging, config,
// auth, LLM client, session directory, context manager, tool registry, MCP,
// hooks, agent, skills, and agent definitions. When asyncMCP is true, MCP
// endpoints are exposed to the UI immediately as pending and connected later in
// the background.
//
// The caller must call ac.Close() when done (typically via defer).
func initApp(asyncMCP bool, mode string, sessionOpts sessionStartupOptions) (*AppContext, error) {
	ac := &AppContext{ACPMode: mode == "acp"}

	// Signal handling and process identity.
	ac.Ctx, ac.Cancel = signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	ac.InstanceID = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

	// Content root and working directory. The content root anchors project
	// content, control-plane configuration, and machine state; it is the main
	// worktree root when chord runs inside a linked worktree. The working
	// directory is the checkout this session works in.
	workDir, err := os.Getwd()
	if err != nil {
		ac.Cancel()
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	contentRoot := resolveContentRoot(ac.Ctx, workDir)
	startupPlan, err := planInitAppStartup(contentRoot, workDir)
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
	logCtx := logContext{PWD: workDir, PID: os.Getpid()}
	ac.logCtx = logCtx

	logPath := filepath.Join(pathLocator.LogsDir, runtimeLogFileName)
	logWriter, logErr := newRotatingLogFile(logPath)
	if logErr == nil {
		ac.LogWriter = logWriter
		logger := newGologLoggerWithContext(logWriter, logLevel, logCtx)
		if redirect, redirectErr := redirectProcessStderr(logWriter.CurrentFile()); redirectErr != nil {
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
	// selection and MainAgent setup. Agent definitions are control-plane
	// configuration, so they come from the content root only.
	agentConfigs, agentConfigsErr := config.ResolveAgentConfigs(
		filepath.Join(contentRoot, ".chord", "agents"),
		filepath.Join(pathLocator.ConfigHome, "agents"),
	)

	if agentConfigsErr == nil && agentConfigs != nil {
		if err := config.ResolveAgentModelPools(agentConfigs, cfg.ModelPools); err != nil {
			agentConfigsErr = err
		}
	}
	if agentConfigsErr == nil {
		agentConfigsErr = config.ValidateAgentMCP(agentConfigs, cfg.MCP)
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
	// Exclusive cross-process ownership was acquired while planning: a session
	// that cannot be owned is never chosen, so --continue falls through to the
	// next candidate instead of failing startup.
	ac.SessionLock = sessionPlan.SessionLock
	ac.StartupSkippedLockedSessions = append([]string(nil), sessionPlan.SkippedLockedIDs...)
	ac.logCtx.SID = filepath.Base(ac.SessionDir)
	if ac.LogWriter != nil {
		logger := newGologLoggerWithContext(ac.LogWriter, ac.logLevel, ac.logCtx)
		setDefaultLogger(logger)
	} else {
		setDefaultLogger(newStderrGologLoggerWithContext(ac.logLevel, ac.logCtx))
	}
	for _, skipped := range sessionPlan.SkippedLockedIDs {
		log.Infof("session %s is open in another Chord process; continuing with %s instead", skipped, ac.logCtx.SID)
	}

	// Sessions of worktrees created by older chord versions live under that
	// worktree's own project key and are no longer listed. Surface them when
	// the user asks to continue a session, so a vanished session is explained
	// instead of silently missing.
	if sessionOpts.ContinueLatest || strings.TrimSpace(sessionOpts.ResumeID) != "" {
		hintCtx := ac.Ctx
		if hintCtx == nil {
			hintCtx = context.Background()
		}
		printAbandonedWorktreeSessionsHint(os.Stderr, findAbandonedWorktreeSessions(hintCtx, ac.ContentRoot), projectLocator.ProjectSessionsDir)
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

	// The worktree container is resolved once at startup and then pinned: it is
	// what Grep/Glob prune so a search never reports another checkout's copies,
	// and what the policy-root container rule derives fresh checkouts from.
	worktreeRoot, err := worktree.WorktreeRoot(ac.PathLocator, contentRoot, cfg.Worktree.Root)
	if err != nil {
		ac.cleanup()
		return nil, err
	}

	// Tool registry.
	ac.Registry = tools.NewRegistry()
	ac.Registry.Register(tools.ReadTool{BaseDir: ac.WorkDir})
	ac.Registry.Register(tools.WriteTool{BaseDir: ac.WorkDir})
	ac.Registry.Register(tools.ApplyPatchTool{BaseDir: ac.WorkDir})
	ac.Registry.Register(tools.EditTool{BaseDir: ac.WorkDir})
	ac.Registry.Register(tools.DeleteTool{BaseDir: ac.WorkDir})

	// Detect shell type and create appropriate ShellTool
	detectedShell, err := shell.DetectShell()
	if err != nil {
		log.Warnf("shell detection failed, using bash as default error=%v", err)
		detectedShell = shell.ShellBash
	}
	log.Debugf("detected shell for command execution shell=%v", detectedShell.String())
	shellTool := tools.NewShellTool(detectedShell.String())
	shellTool.BaseDir = ac.WorkDir
	ac.Registry.Register(shellTool)

	ac.Registry.Register(tools.JobOutputTool{})
	ac.Registry.Register(tools.JobListTool{})
	ac.Registry.Register(tools.JobKillTool{})
	ac.Registry.Register(tools.GrepTool{BaseDir: ac.WorkDir, WorktreeRoot: worktreeRoot})
	ac.Registry.Register(tools.GlobTool{BaseDir: ac.WorkDir, WorktreeRoot: worktreeRoot})
	ac.Registry.Register(tools.HandoffTool{BaseDir: ac.WorkDir})
	ac.Registry.Register(tools.NewWebFetchTool(cfg.WebFetch, cfg.Proxy))

	// MCP servers.
	mcpConfigs := mcp.ServerConfigsFromConfig(cfg.MCP)
	syncMCPPromptBlock := ""
	if len(mcpConfigs) > 0 {
		if asyncMCP {
			ac.MCPConfigs = mcpConfigs
			ac.MCPMgr = mcp.NewPendingManagerWithClientInfo(mcpConfigs, mcp.ClientInfo{Name: "chord", Version: Version})
			ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)
		} else {
			mgr, err := mcp.NewManagerWithClientInfo(ac.Ctx, mcpConfigs, mcp.ClientInfo{Name: "chord", Version: Version})
			if err != nil {
				log.Warnf("MCP initialization failed error=%v", err)
			} else {
				ac.MCPMgr = mgr
				ac.MCPCatalog = mcp.NewCatalog(mgr)
				ac.MCPConfigs = mcpConfigs
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
		ac.SessionDir, modelID, contentRoot, workDir,
		cfg, ac.ProjectCfg,
		mcp.ClientInfo{Name: "chord", Version: Version},
		ac.PathLocator,
	)
	llmClient.SetSessionID(filepath.Base(ac.SessionDir))
	ac.MainAgent.SetInitialYoloMode(flagYolo)
	worktreeBranchPrefix := resolveWorktreeBranchPrefix(cfg)
	ac.MainAgent.SetPathRootsResolver(newPathRootsResolver(ac.Ctx, contentRoot, ac.PathLocator, cfg.Worktree.Root))
	ac.MainAgent.SetWorktreeRuntime(agent.WorktreeRuntime{
		PathLocator:  ac.PathLocator,
		RepoRoot:     contentRoot,
		BranchPrefix: worktreeBranchPrefix,
		Root:         cfg.Worktree.Root,
		SessionID:    filepath.Base(ac.SessionDir),
		RebindLSP:    ac.rebindLSPForWorkDir,
		RefreshSkills: func(workDir string) {
			refreshSkillsForWorkDir(ac, workDir)
		},
	})
	// A session remembers the checkout it works in. Stamp the agent's binding
	// so WorktreeList, Exit and the completion reports agree with the directory
	// the session was started (or resumed) in; when the recorded checkout is
	// gone the agent falls back and the notice is shown once as a toast.
	if flagWorktreeResumeNotice != "" {
		ac.MainAgent.SetStartupWorkDirNotice(flagWorktreeResumeNotice)
	}
	if info := flagWorktreeStartupInfo; info != nil {
		reason := flagWorktreeStartupReason
		if reason == "" {
			reason = recovery.WorktreeSwitchResume
		}
		notice := ac.MainAgent.RestoreWorkDirBinding(ac.Ctx, agent.WorkDirState{
			Path:       info.Path,
			WorktreeID: info.Name,
			Branch:     info.Branch,
			BaseSHA:    info.BaseSHA,
		}, reason)
		if notice != "" {
			fmt.Fprintln(os.Stderr, "warning: "+notice)
			ac.MainAgent.SetStartupWorkDirNotice(notice)
		}
	}
	ac.MainAgent.SetSessionLock(ac.SessionLock)
	ac.MainAgent.SetStartupSkippedLockedSessions(ac.StartupSkippedLockedSessions)
	ac.MainAgent.SetStartupConfigIssues(collectStartupConfigIssues(startupPlan))
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

		// Reconcile MCP connections with the target session's persisted manual
		// enabled intent. Skipped before runtime MCP startup (the startup path
		// handles it) so resume-at-startup does not race startRuntimeMCP.
		if ac.mcpRuntimeStarted.Load() {
			restoreMCPSessionIntentAsync(ac)
		}
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
	viewImageTool.BaseDir = ac.WorkDir
	ac.Registry.Register(viewImageTool)
	// Worktree tools are rebound per agent: a SubAgent gets its own instances
	// so entering a worktree switches only that sub-agent's working directory.
	ac.Registry.Register(tools.NewWorktreeEnterTool(ac.MainAgent))
	ac.Registry.Register(tools.NewWorktreeExitTool(ac.MainAgent))
	ac.Registry.Register(tools.NewWorktreeListTool(ac.MainAgent))

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

	if !asyncMCP && len(mcpConfigs) > 0 {
		result, err := loadSynchronousMCPState(ac.Ctx, ac)
		if err != nil {
			log.Warnf("MCP synchronous startup incomplete error=%v", err)
		}
		for _, t := range result.Tools {
			ac.Registry.Register(t)
			log.Debugf("registered MCP tool name=%v", t.Name())
		}
		syncMCPPromptBlock = result.PromptBlock
		// Synchronous startup already connected and discovered MCP. Consume the
		// async starter so createRuntime does not reconnect every server.
		ac.mcpRuntimeStarted.Store(true)
		ac.mcpStartOnce.Do(func() {})
	}
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
	if ac.ProviderCache != nil {
		ac.ProviderCache.close()
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
