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
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/tui"
)

// CLI flags (bound in main; consumed by initApp).
var (
	flagAPIBase         string
	flagContinueSession bool
	flagResumeSession   string

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

func main() {
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

	rootCmd.AddCommand(newAuthCmd(), newHeadlessCmd(), newTestProvidersCmd(), newCleanupCmd())

	if err := rootCmd.Execute(); err != nil {
		// context.Canceled is expected on signal-driven shutdown — exit cleanly.
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
}

// runRoot is the main execution path for TUI mode. In local mode, the TUI
// runs in-process with the MainAgent — no IPC, no socket, no server spawn.
func runRoot(_ *cobra.Command, _ []string) error {
	resumeID := strings.TrimSpace(flagResumeSession)
	if flagContinueSession && resumeID != "" {
		return fmt.Errorf("--continue and --resume are mutually exclusive")
	}

	// pprof: enabled only when CHORD_PPROF_PORT is set (e.g. "6060").
	// It always binds to 127.0.0.1 for local-only diagnostics.
	// The import of _ "net/http/pprof" registers handlers on http.DefaultServeMux
	// at init time but has no runtime overhead unless a server is started here.
	if addr, err := resolvePprofListenAddr(); err != nil {
		slog.Warn("invalid CHORD_PPROF_PORT; pprof disabled", "error", err)
	} else if addr != "" {
		go func() {
			slog.Info("pprof listening", "addr", addr)
			if err := http.ListenAndServe(addr, nil); err != nil {
				slog.Warn("pprof server exited", "error", err)
			}
		}()
	}

	// 1. Initialize the full application context (config, auth, LLM, agent, tools, etc.)
	ac, err := initApp(true, "local", sessionStartupOptions{
		ContinueLatest: flagContinueSession,
		ResumeID:       resumeID,
	})
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

	// 2. Prepare the TUI model against the current MainAgent state before the
	// runtime consumes startup-resume pending state. This keeps the first frame's
	// loading/disabled UI accurate for --continue/--resume startup.
	var opts []tea.ProgramOption
	terminalOut := os.Stdout
	if !term.IsTerminal(os.Stdin.Fd()) {
		ttyIn, ttyOut, err := tea.OpenTTY()
		if err != nil {
			return fmt.Errorf("chord requires a terminal (TTY): %w", err)
		}
		terminalOut = ttyOut
		opts = append(opts, tea.WithInput(ttyIn))
	}
	if term.IsTerminal(terminalOut.Fd()) {
		opts = append(opts, tea.WithOutput(tui.WrapTerminalImageOutput(terminalOut)))
	}

	initialWidth, initialHeight := 80, 24
	if term.IsTerminal(terminalOut.Fd()) {
		if w, h, err := term.GetSize(terminalOut.Fd()); err == nil && w > 0 && h > 0 {
			initialWidth, initialHeight = w, h
		}
	}
	model := tui.NewModelWithSize(ac.MainAgent, initialWidth, initialHeight)
	tuiModel := model
	tuiModel.SetInstanceID(ac.InstanceID)
	if ac.Cfg != nil {
		if len(ac.Cfg.KeyMap) > 0 {
			tuiModel.SetKeyMap(tui.KeyMapFromConfig(ac.Cfg.KeyMap))
		}
		if ac.Cfg.IMESwitchTarget != "" {
			tuiModel.SetIMESwitchTarget(ac.Cfg.IMESwitchTarget)
		}
		tui.SetSingleLineDiffColumnsLimit(ac.Cfg.Diff.InlineMaxColumns)
		if len(ac.LoadedCommands) > 0 {
			tuiCmds := make([]tui.CustomCommand, len(ac.LoadedCommands))
			for i, d := range ac.LoadedCommands {
				tuiCmds[i] = tui.CustomCommand{Cmd: "/" + d.Name, Desc: d.Description}
			}
			tuiModel.SetCustomCommands(tuiCmds)
		}
	}
	if term.IsTerminal(terminalOut.Fd()) {
		osc9 := false
		if ac.Cfg != nil && ac.Cfg.DesktopNotification != nil {
			osc9 = *ac.Cfg.DesktopNotification
		}
		tuiModel.SetDesktopNotification(osc9, terminalOut)
	}

	// 3. Wire up confirmFn, questionFn, QuestionTool, LSP/MCP status, start event loop.
	rt, err = createRuntime(ac)
	if err != nil {
		return err
	}

	// 4. Run the TUI directly against the in-process MainAgent.
	opts = append(opts, tea.WithWindowSize(initialWidth, initialHeight))

	p := tea.NewProgram(&tuiModel, opts...)
	go func() {
		<-ac.Ctx.Done()
		p.Quit()
	}()

	_, tuiErr := p.Run()
	if err := tuiModel.Close(); err != nil {
		slog.Warn("tui runtime cache cleanup failed", "error", err)
	}

	// 4. Graceful shutdown: cancel the current turn and let the agent wind down.
	shutdownLocalRuntime(ac, rt, localExitIdleWait)
	rtClosed = true
	acClosed = true
	if summary := ac.MainAgent.GetSessionSummary(); summary != nil && sessionDirHasMessages(ac.SessionDir) {
		printResumeHint(summary.ID)
	}

	if tuiErr != nil && !errors.Is(tuiErr, context.Canceled) {
		return tuiErr
	}
	return nil
}

func shutdownLocalRuntime(ac *AppContext, rt *Runtime, waitTimeout time.Duration) {
	shutdownLocalRuntimeForTest(
		ac,
		rt,
		waitTimeout,
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
	cancelCurrentTurn func() bool,
	waitIdle func(time.Duration) bool,
	closeRuntime func(),
	closeApp func(),
) {
	if cancelCurrentTurn != nil && cancelCurrentTurn() && waitIdle != nil {
		waitIdle(waitTimeout)
	}
	if closeRuntime != nil {
		closeRuntime()
	}
	if closeApp != nil {
		closeApp()
	}
}

func printResumeHint(sessionID string) {
	if sessionID == "" {
		return
	}
	if term.IsTerminal(os.Stderr.Fd()) && os.Getenv("NO_COLOR") == "" {
		fmt.Fprintf(os.Stderr, "\n\x1b[90mResume this session with:\x1b[0m\n\x1b[90mchord -r %s\x1b[0m\n", sessionID)
		return
	}
	fmt.Fprintf(os.Stderr, "\nResume this session with:\nchord -r %s\n", sessionID)
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
	var provName string
	ref = config.NormalizeModelRef(ref)

	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 2 {
		provName = parts[0]
		resolvedModelID = parts[1]
	} else {
		// No provider prefix: search all providers for this model.
		resolvedModelID = ref
		provNames := make([]string, 0, len(allProviders))
		for name := range allProviders {
			provNames = append(provNames, name)
		}
		sort.Strings(provNames)
		for _, name := range provNames {
			if _, ok := allProviders[name].Models[resolvedModelID]; ok {
				provName = name
				break
			}
		}
		if provName == "" {
			return nil, nil, "", 0, 0, fmt.Errorf("model %q not found in any provider", ref)
		}
	}

	providerCfg, ok := allProviders[provName]
	if !ok {
		return nil, nil, "", 0, 0, fmt.Errorf("provider %q not found in config", provName)
	}

	mc, ok := providerCfg.Models[resolvedModelID]
	if !ok {
		return nil, nil, "", 0, 0, fmt.Errorf("model %q not found in provider %q", resolvedModelID, provName)
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
				effectiveProxy := llm.ResolveEffectiveProxy(normalizedProviderCfg.Proxy, globalProxy)
				oauthMap, backfills := oauthCredentialMap(creds)
				provCfg.SetOAuthRefresher(
					tokenURL,
					clientID,
					authConfigPath,
					&authCopy,
					&authMu,
					oauthMap,
					effectiveProxy,
				)
				if len(backfills) > 0 {
					if saveErr := persistOAuthMetadataBackfills(authConfigPath, &authCopy, &authMu, provName, backfills); saveErr != nil {
						slog.Warn("failed to persist backfilled OAuth email/account_id", "provider", provName, "error", saveErr)
					}
				}
				provCfg.StartCodexRateLimitPolling(func(key, accountID string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	default:
		return nil, nil, "", 0, 0, fmt.Errorf("unsupported provider type %q for %q (allowed: %s, %s, %s)",
			normalizedProviderCfg.Type, ref,
			config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses)
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

func newTestProvidersCmd() *cobra.Command {
	var providerFilter string
	cmd := &cobra.Command{
		Use:   "test-providers",
		Short: "Test all configured providers with a simple request",
		Long: `Run a lightweight provider smoke test using the real runtime transport path.

This command verifies config loading, auth availability, proxy wiring, provider
construction, and whether a minimal request can complete successfully. For
Responses-based providers it also prints the final transport used, such as
"websocket" or "http".

This is a connectivity and transport diagnostic, not a full end-to-end agent
acceptance test. It does not validate tools, long-running session behavior,
context compaction, or broader orchestration semantics.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return testProviders(providerFilter)
		},
	}
	cmd.Flags().StringVar(&providerFilter, "provider", "", "Provider name to test")
	return cmd
}
