// Package main is the CLI entry point for chord.
//
// The default command runs the TUI in local mode: the MainAgent runs in-process
// with the TUI, no IPC needed. The "headless" subcommand starts a stdio
// JSON control-plane server for bot/gateway integration (see cmd/chord/headless.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keakon/golog/log"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/worktree"
)

// CLI flags (bound in main; consumed by initApp).
var (
	flagAPIBase         string
	flagContinueSession bool
	flagResumeSession   string
	flagWorktree        string

	// Path policy overrides (CLI > env > config.yaml paths.* > XDG defaults).
	// These map to CHORD_* env vars so internal config/path resolvers stay centralized.
	flagConfigHome  string
	flagConfig      string // alias of --config-home
	flagStateDir    string
	flagCacheDir    string
	flagSessionsDir string
	flagLogsDir     string
)

const localExitIdleWait = 500 * time.Millisecond

func resolvePprofListenAddr() (string, error) {
	portRaw := strings.TrimSpace(os.Getenv("CHORD_PPROF_PORT"))
	if portRaw == "" {
		return "", nil
	}
	portRaw = strings.TrimPrefix(portRaw, ":")
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid CHORD_PPROF_PORT %q (expected 1-65535)", portRaw)
	}
	return fmt.Sprintf("127.0.0.1:%d", port), nil
}

type cliExitError struct {
	code int
	err  error
}

func (e cliExitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("exit code %d", e.code)
}

func (e cliExitError) Unwrap() error { return e.err }

func cliExitCode(err error) int {
	var exitErr cliExitError
	if errors.As(err, &exitErr) {
		return exitErr.code
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	return 1
}

func shouldPrintCLIError(err error) bool {
	if err == nil {
		return false
	}
	var exitErr cliExitError
	if errors.As(err, &exitErr) && exitErr.code == 130 {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return true
}

var initAppRunner = initApp

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "chord",
		Short:         "AI coding assistant with multi-agent orchestration",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Apply CLI path overrides as env vars so they participate in the
			// unified path policy resolution across all commands.
			if strings.TrimSpace(flagConfigHome) != "" && strings.TrimSpace(flagConfig) != "" && strings.TrimSpace(flagConfigHome) != strings.TrimSpace(flagConfig) {
				return fmt.Errorf("--config-home and --config specify different values")
			}
			if strings.TrimSpace(flagConfigHome) == "" {
				flagConfigHome = flagConfig
			}
			if strings.TrimSpace(flagConfigHome) != "" {
				os.Setenv("CHORD_CONFIG_HOME", strings.TrimSpace(flagConfigHome))
			}
			if strings.TrimSpace(flagStateDir) != "" {
				os.Setenv("CHORD_STATE_DIR", strings.TrimSpace(flagStateDir))
			}
			if strings.TrimSpace(flagCacheDir) != "" {
				os.Setenv("CHORD_CACHE_DIR", strings.TrimSpace(flagCacheDir))
			}
			if strings.TrimSpace(flagSessionsDir) != "" {
				os.Setenv("CHORD_SESSIONS_DIR", strings.TrimSpace(flagSessionsDir))
			}
			if strings.TrimSpace(flagLogsDir) != "" {
				os.Setenv("CHORD_LOGS_DIR", strings.TrimSpace(flagLogsDir))
			}
			return nil
		},
		RunE: runRoot,
	}
	rootCmd.SetVersionTemplate(cliVersionTemplate())

	rootCmd.PersistentFlags().StringVar(&flagAPIBase, "api-base", "",
		"API base URL (overrides CHORD_API_BASE env var)")

	// Storage path overrides (see docs/guides/session-storage.md).
	rootCmd.PersistentFlags().StringVar(&flagConfigHome, "config-home", "",
		"Config home directory (overrides CHORD_CONFIG_HOME)")
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "",
		"Alias of --config-home")
	_ = rootCmd.PersistentFlags().MarkHidden("config")
	rootCmd.PersistentFlags().StringVar(&flagStateDir, "state-dir", "",
		"State directory (overrides CHORD_STATE_DIR and config.yaml paths.state_dir)")
	rootCmd.PersistentFlags().StringVar(&flagCacheDir, "cache-dir", "",
		"Cache directory (overrides CHORD_CACHE_DIR and config.yaml paths.cache_dir)")
	rootCmd.PersistentFlags().StringVar(&flagSessionsDir, "sessions-dir", "",
		"Sessions root directory (overrides CHORD_SESSIONS_DIR and config.yaml paths.sessions_dir)")
	rootCmd.PersistentFlags().StringVar(&flagLogsDir, "logs-dir", "",
		"Logs directory (overrides CHORD_LOGS_DIR and config.yaml paths.logs_dir)")
	rootCmd.Flags().BoolVarP(&flagContinueSession, "continue", "c", false,
		"Continue the latest non-empty session in the current project")
	rootCmd.Flags().StringVarP(&flagResumeSession, "resume", "r", "",
		"Resume a specific session ID in the current project")
	rootCmd.Flags().StringVarP(&flagWorktree, "worktree", "w", "",
		"Create or enter a chord-managed git worktree by name (auto-named when empty); session/cache live under the worktree's project key")
	rootCmd.Flags().Lookup("worktree").NoOptDefVal = ""

	rootCmd.AddCommand(newAuthCmd(), newHeadlessCmd(), newDoctorCmd(), newCleanupCmd(), newWorktreeCmd(), newResumeCmd(), newImportCmd())
	return rootCmd
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	rootCmd := newRootCmd()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		if shouldPrintCLIError(err) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(cliExitCode(err))
	}
}

// runRoot is the main execution path for TUI mode. In local mode, the TUI
// runs in-process with the MainAgent — no IPC, no socket, no server spawn.
func runRoot(cmd *cobra.Command, _ []string) error {
	plan, err := planRootStartup(cmd, flagContinueSession, flagResumeSession, flagWorktree)
	if err != nil {
		if strings.Contains(err.Error(), "CHORD_PPROF_PORT") {
			log.Warnf("invalid CHORD_PPROF_PORT; pprof disabled error=%v", err)
		} else {
			return err
		}
	}

	if plan.RunSetupWizard {
		if err := maybeRunInitialSetupWizard(cmd); err != nil {
			return err
		}
	}

	// --worktree: create/enter the worktree before initApp so the rest of
	// the startup sees it as the project root. flagWorktreeStartupInfo is
	// nil when entered via the worktree resume subcommand or another path
	// that has already prepared the worktree.
	if plan.PrepareWorktree {
		wtCtx := cmd.Context()
		if wtCtx == nil {
			wtCtx = context.Background()
		}
		info, err := prepareStartupWorktree(wtCtx, plan.WorktreeName)
		if err != nil {
			return err
		}
		flagWorktreeStartupInfo = info
		flagWorktreeStartupMeta = worktreeMetaForInfo(info)
		plan.SessionOptions.NewSessionMeta = flagWorktreeStartupMeta
	}

	// pprof: enabled only when CHORD_PPROF_PORT is set (e.g. "6060").
	// It always binds to 127.0.0.1 for local-only diagnostics.
	// The import of _ "net/http/pprof" registers handlers on http.DefaultServeMux
	// at init time but has no runtime overhead unless a server is started here.
	if plan.PprofListenAddr != "" {
		go func() {
			log.Infof("pprof listening addr=%v", plan.PprofListenAddr)
			if err := http.ListenAndServe(plan.PprofListenAddr, nil); err != nil {
				log.Warnf("pprof server exited error=%v", err)
			}
		}()
	}

	// Initialize the full application context (config, auth, LLM, agent, tools, etc.).
	ac, err := initAppRunner(true, "local", plan.SessionOptions)
	if err != nil {
		return err
	}
	var rt *Runtime
	acClosed := false
	rtClosed := false
	defer func() {
		if !rtClosed && rt != nil {
			rt.Close()
		}
		if !acClosed {
			ac.Close()
		}
	}()

	// Prepare the TUI model against the current MainAgent state before the
	// runtime consumes startup-resume pending state. This keeps the first frame's
	// loading/disabled UI accurate for --continue/--resume startup.
	programPlan, err := defaultTUIProgramFactory().build(ac)
	if err != nil {
		return err
	}
	tuiModel := programPlan.model

	// Wire up confirmFn, questionFn, QuestionTool, LSP/MCP status, and start the event loop.
	rt, err = createRuntime(ac)
	if err != nil {
		return err
	}

	// Run the TUI directly against the in-process MainAgent.
	go func() {
		<-ac.Ctx.Done()
		programPlan.runner.Quit()
	}()

	_, tuiErr := programPlan.runner.Run()
	if err := tuiModel.Close(); err != nil {
		log.Warnf("tui runtime cache cleanup failed error=%v", err)
	}

	// Graceful shutdown: when Done completed and the agent closed as expected,
	// avoid re-cancelling an already completed turn during local runtime teardown.
	skipCancelOnShutdown := tuiModel.ExpectedAgentCloseCompleted()
	shutdownLocalRuntime(ac, rt, localExitIdleWait, skipCancelOnShutdown)
	rtClosed = true
	acClosed = true
	if summary := ac.MainAgent.GetSessionSummary(); summary != nil && sessionDirHasMessages(ac.SessionDir) {
		printResumeHint(ac, summary.ID)
	}

	if tuiErr != nil && !errors.Is(tuiErr, context.Canceled) {
		return tuiErr
	}
	return nil
}

func shutdownLocalRuntime(ac *AppContext, rt *Runtime, waitTimeout time.Duration, skipCancel bool) {
	shutdownLocalRuntimeForTest(
		ac,
		rt,
		waitTimeout,
		skipCancel,
		func() bool {
			return rt != nil && rt.Agent != nil && rt.Agent.CancelCurrentTurn()
		},
		func(timeout time.Duration) bool {
			if rt == nil {
				return true
			}
			return rt.WaitIdleOrTimeout(timeout)
		},
		func() {
			if rt != nil {
				rt.Close()
			}
		},
		func() {
			if ac != nil {
				ac.Close()
			}
		},
	)
}

func shutdownLocalRuntimeForTest(
	ac *AppContext,
	rt *Runtime,
	waitTimeout time.Duration,
	skipCancel bool,
	cancelCurrentTurn func() bool,
	waitIdle func(time.Duration) bool,
	closeRuntime func(),
	closeApp func(),
) {
	if !skipCancel && cancelCurrentTurn != nil && cancelCurrentTurn() && waitIdle != nil {
		waitIdle(waitTimeout)
	}
	if closeRuntime != nil {
		closeRuntime()
	}
	if closeApp != nil {
		closeApp()
	}
}

func printResumeHint(ac *AppContext, sessionID string) {
	cmd := resumeHintCommand(ac, sessionID)
	if cmd == "" {
		return
	}
	if term.IsTerminal(os.Stderr.Fd()) && os.Getenv("NO_COLOR") == "" {
		fmt.Fprintf(os.Stderr, "\n\x1b[90mResume this session with:\x1b[0m\n\x1b[90m%s\x1b[0m\n", cmd)
		return
	}
	fmt.Fprintf(os.Stderr, "\nResume this session with:\n%s\n", cmd)
}

func resumeHintCommand(ac *AppContext, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	if wt := activeWorktreeInfo(ac); wt != nil && wt.Name != "" {
		return worktreeResumeCommand(wt.Name, sessionID)
	}
	return fmt.Sprintf("chord --resume %s", sessionID)
}

func worktreeResumeCommand(worktreeName, sessionID string) string {
	worktreeName = strings.TrimSpace(worktreeName)
	sessionID = strings.TrimSpace(sessionID)
	if worktreeName == "" || sessionID == "" {
		return ""
	}
	switch worktreeName {
	case "list", "remove", "finish":
		return fmt.Sprintf("chord --worktree %s --resume %s", worktreeName, sessionID)
	default:
		return fmt.Sprintf("chord worktree %s --resume %s", worktreeName, sessionID)
	}
}

func activeWorktreeInfo(ac *AppContext) *worktree.Info {
	if flagWorktreeStartupInfo != nil {
		return flagWorktreeStartupInfo
	}
	if ac == nil || ac.PathLocator == nil || ac.ProjectLocator == nil || strings.TrimSpace(ac.ProjectRoot) == "" {
		return nil
	}
	ctx := ac.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	mainRoot, err := worktree.GitMainRoot(ctx, ac.ProjectRoot)
	if err != nil {
		return nil
	}
	if samePath(mainRoot, ac.ProjectRoot) {
		return nil
	}
	repoID := worktree.RepoIDFor(mainRoot)
	idx, err := worktree.LoadRepoIndex(ac.PathLocator.StateDir, repoID)
	if err != nil || idx == nil {
		return nil
	}
	for i := range idx.Worktrees {
		entry := &idx.Worktrees[i]
		if samePath(entry.Path, ac.ProjectRoot) {
			return &worktree.Info{
				Name:     entry.Name,
				Slug:     entry.Slug,
				Branch:   entry.Branch,
				Path:     entry.Path,
				RepoRoot: mainRoot,
				RepoID:   repoID,
			}
		}
	}
	return nil
}

func samePath(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	return a == b
}

func sessionDirHasMessages(sessionDir string) bool {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(sessionDir, "main.jsonl"))
	return err == nil && info.Size() > 0
}

// getProviderFunc returns a ProviderConfig for the given provider; used to
// share provider config (and key cooldown state) across sessions.
type getProviderFunc func(provName string, cfg config.ProviderConfig, apiKeys []string) (*llm.ProviderConfig, error)

// getProviderImplFunc returns a Provider implementation for the given provider;
// callers may supply a cache-backed getter so transport runtime state (e.g.
// Responses WebSocket sticky-disable state) is shared per provider.
// For Codex this is correctness-sensitive, not just an optimization: WS
// incremental state is connection-scoped and therefore must survive client
// rebuilds within the same provider runtime.
type getProviderImplFunc func(provName string, cfg config.ProviderConfig, providerCfg *llm.ProviderConfig, modelID string) (llm.Provider, error)

// resolveModelRef parses a model reference string and returns the components
// needed to create an LLM client for that model. The reference format is
// "providerName/modelID[@variant]" (or just "modelID[@variant]"). Any inline
// @variant suffix is ignored here; callers are responsible for applying the
// variant to the constructed client.
//
// Returns:
//   - provCfg:      the ProviderConfig for LLM client construction
//   - impl:         the Provider implementation (Anthropic, OpenAI, etc.)
//   - modelID:      the resolved model ID
//   - maxTokens:    the model's max output tokens
//   - contextLimit: the model's context window limit
//   - err:          non-nil if the reference could not be resolved
func resolveModelRef(
	parentCtx context.Context,
	ref string,
	allProviders map[string]config.ProviderConfig,
	auth config.AuthConfig,
	globalProxy string,
	getProvider getProviderFunc,
	getProviderImpl getProviderImplFunc,
) (
	provCfg *llm.ProviderConfig,
	impl llm.Provider,
	resolvedModelID string,
	maxTokens int,
	contextLimit int,
	err error,
) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	var (
		provName    string
		providerCfg config.ProviderConfig
		mc          config.ModelConfig
	)
	provName, resolvedModelID, _, providerCfg, mc, err = config.ResolveConfiguredModelRef(allProviders, ref)
	if err != nil {
		return nil, nil, "", 0, 0, err
	}

	creds := auth[provName]
	apiKeys := config.ExtractAPIKeys(creds)
	// Note: empty creds is allowed for local/self-hosted providers that don't require authentication.
	// The provider implementation will handle authentication as needed.
	normalizedProviderCfg, err := normalizeProviderConfig(provName, providerCfg, creds)
	if err != nil {
		return nil, nil, "", 0, 0, err
	}

	if getProvider == nil {
		provCfg = llm.NewProviderConfig(provName, normalizedProviderCfg, apiKeys)
		if tokenURL, clientID, ok, err := resolveProviderOAuthSettings(normalizedProviderCfg, creds); err != nil {
			return nil, nil, "", 0, 0, fmt.Errorf("resolve OAuth settings for provider %q: %w", provName, err)
		} else if ok {
			authConfigPath, pathErr := config.AuthPath()
			if pathErr == nil {
				authCopy := auth
				var authMu sync.Mutex
				authStatePath := strings.TrimSuffix(authConfigPath, ".yaml") + ".state.yaml"
				effectiveProxy := llm.ResolveEffectiveProxy(normalizedProviderCfg.Proxy, globalProxy)
				oauthMap, backfills := oauthCredentialMap(creds)
				provCfg.SetOAuthRefresher(
					tokenURL,
					clientID,
					authConfigPath,
					authStatePath,
					&authCopy,
					&authMu,
					oauthMap,
					effectiveProxy,
				)
				if len(backfills) > 0 {
					if saveErr := persistOAuthMetadataBackfills(authConfigPath, &authCopy, &authMu, provName, backfills); saveErr != nil {
						log.Warnf("failed to persist backfilled OAuth email/account_id provider=%v error=%v", provName, saveErr)
					}
				}
				provCfg.StartCodexRateLimitPolling(func(key, accountID string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
					ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
					defer cancel()
					return llm.FetchCodexUsageSnapshot(ctx, provCfg, key, accountID)
				})
			}
		}
	} else {
		provCfg, err = getProvider(provName, normalizedProviderCfg, apiKeys)
		if err != nil {
			return nil, nil, "", 0, 0, err
		}
	}

	// Wire rate limiting if configured.
	if normalizedProviderCfg.RateLimit > 0 {
		provCfg.SetRateLimiter(normalizedProviderCfg.RateLimit)
	}

	// Reuse provider implementations when a cache-backed getter is available so
	// transport runtime state (e.g. Responses WebSocket chain + sticky-disable)
	// is preserved across clients, fallback models, and policy rebuilds.
	if getProviderImpl != nil {
		impl, err = getProviderImpl(provName, normalizedProviderCfg, provCfg, resolvedModelID)
		if err != nil {
			return nil, nil, "", 0, 0, err
		}
		return provCfg, impl, resolvedModelID, mc.Limit.Output, mc.Limit.Context, nil
	}

	// Provider type already normalized during config load
	providerType := normalizedProviderCfg.Type
	effectiveProxy := llm.ResolveEffectiveProxy(normalizedProviderCfg.Proxy, globalProxy)

	switch providerType {
	case config.ProviderTypeChatCompletions:
		p, pErr := llm.NewOpenAIProvider(provCfg, effectiveProxy)
		if pErr != nil {
			return nil, nil, "", 0, 0, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeChatCompletions, ref, pErr)
		}
		impl = p
	case config.ProviderTypeMessages:
		p, pErr := llm.NewAnthropicProvider(provCfg, effectiveProxy)
		if pErr != nil {
			return nil, nil, "", 0, 0, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeMessages, ref, pErr)
		}
		impl = p
	case config.ProviderTypeResponses:
		p, pErr := llm.NewResponsesProvider(provCfg, effectiveProxy)
		if pErr != nil {
			return nil, nil, "", 0, 0, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeResponses, ref, pErr)
		}
		impl = p
	case config.ProviderTypeGenerateContent:
		p, pErr := llm.NewGeminiProvider(provCfg, effectiveProxy)
		if pErr != nil {
			return nil, nil, "", 0, 0, fmt.Errorf("create %s provider for %q: %w", config.ProviderTypeGenerateContent, ref, pErr)
		}
		impl = p
	default:
		return nil, nil, "", 0, 0, fmt.Errorf("unsupported provider type %q for %q (allowed: %s, %s, %s, %s)",
			normalizedProviderCfg.Type, ref,
			config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses, config.ProviderTypeGenerateContent)
	}

	return provCfg, impl, resolvedModelID, mc.Limit.Output, mc.Limit.Context, nil
}

// hookDefsFromConfig converts the grouped HookConfig from the YAML config
// into a flat list of hook.HookDef suitable for hook.NewCommandEngineFromList.
func hookDefsFromConfig(hc config.HookConfig) []hook.HookDef {
	var defs []hook.HookDef

	addEntries := func(point string, entries []config.HookEntry) {
		for i, e := range entries {
			if e.Command.IsZero() {
				continue
			}
			name := e.Name
			if name == "" {
				name = fmt.Sprintf("%s-%d", point, i)
			}
			defs = append(defs, hook.HookDef{
				Name:            name,
				Point:           point,
				Command:         hook.Command{Shell: e.Command.Shell, Args: append([]string(nil), e.Command.Args...)},
				Timeout:         e.Timeout,
				Tools:           append([]string(nil), e.Tools...),
				Paths:           append([]string(nil), e.Paths...),
				Agents:          append([]string(nil), e.Agents...),
				AgentKinds:      append([]string(nil), e.AgentKinds...),
				Models:          append([]string(nil), e.Models...),
				MinChangedFiles: e.MinChangedFiles,
				OnlyOnError:     e.OnlyOnError,
				Join:            e.Join,
				Result:          e.Result,
				ResultFormat:    e.ResultFormat,
				MaxResultLines:  e.MaxResultLines,
				MaxResultBytes:  e.MaxResultBytes,
				DebounceMS:      e.DebounceMS,
				Concurrency:     e.Concurrency,
				RetryOnFailure:  e.RetryOnFailure,
				RetryDelayMS:    e.RetryDelayMS,
				Environment:     e.Environment,
			})
		}
	}

	addEntries(hook.OnToolCall, hc.OnToolCall)
	addEntries(hook.OnToolResult, hc.OnToolResult)
	addEntries(hook.OnBeforeToolResultAppend, hc.OnBeforeToolResultAppend)
	addEntries(hook.OnBeforeLLMCall, hc.OnBeforeLLMCall)
	addEntries(hook.OnAfterLLMCall, hc.OnAfterLLMCall)
	addEntries(hook.OnBeforeCompress, hc.OnBeforeCompress)
	addEntries(hook.OnAfterCompress, hc.OnAfterCompress)
	addEntries(hook.OnSessionStart, hc.OnSessionStart)
	addEntries(hook.OnSessionEnd, hc.OnSessionEnd)
	addEntries(hook.OnIdle, hc.OnIdle)
	addEntries(hook.OnWaitConfirm, hc.OnWaitConfirm)
	addEntries(hook.OnWaitQuestion, hc.OnWaitQuestion)
	addEntries(hook.OnAgentError, hc.OnAgentError)
	addEntries(hook.OnToolBatchComplete, hc.OnToolBatchComplete)

	return defs
}
