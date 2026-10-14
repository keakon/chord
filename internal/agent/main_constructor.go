package agent

import (
	"context"
	"crypto/rand"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/agent/promptcache"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

// ---------------------------------------------------------------------------
// System prompt
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewMainAgent creates a fully-initialised MainAgent. The caller must invoke
// Run in a separate goroutine to start the event loop.
//
// contentRoot is the root the project's content and machine state are anchored
// to: configuration, agent definitions, skills, memory, AGENTS.md, and the
// session project key. In a linked worktree it is the main worktree root, so
// every checkout of one repository shares them.
//
// The runtime working directory (the checkout tools and shell commands run in)
// comes from the process working directory at construction time.
//
// globalCfg is the user-level config (~/.config/chord/config.yaml). projectCfg is the
// project-level config (.chord/config.yaml); either may be nil.
//
// pathLocators optionally carries the startup-resolved config.PathLocator so
// Memory state files and session scanning honor custom paths.state_dir /
// paths.sessions_dir settings. When omitted the default path locator is used
// (tests construct the agent directly and rely on that).
func NewMainAgent(
	ctx context.Context,
	llmClient *llm.Client,
	ctxMgr *ctxmgr.Manager,
	toolRegistry *tools.Registry,
	hookEngine hook.Manager,
	sessionDir string,
	modelName string,
	contentRoot string,
	workDir string,
	globalCfg *config.Config,
	projectCfg *config.Config,
	mcpClientInfo mcp.ClientInfo,
	pathLocators ...*config.PathLocator,
) *MainAgent {
	var pathLocator *config.PathLocator
	if len(pathLocators) > 0 {
		pathLocator = pathLocators[0]
	}
	parentCtx, cancel := context.WithCancel(ctx)

	if strings.TrimSpace(workDir) == "" {
		workDir = contentRoot
	}
	gitStatusReady := make(chan struct{})
	orchestrationCfg := effectiveOrchestrationConfig(globalCfg, projectCfg)
	governor := newResourceGovernor(orchestrationCfg)

	a := &MainAgent{
		skillPublisher:          newSkillDiscoveryPublisher(skillDiscoveryPublisherOptions{}),
		pause:                   sessionPauseRuntime{inputPrefix: rand.Text()},
		executionGate:           &executiongate.Gate{},
		parentCtx:               parentCtx,
		cancel:                  cancel,
		llmClient:               llmClient,
		ctxMgr:                  ctxMgr,
		tools:                   toolRegistry,
		hookEngine:              hookEngine,
		usageTracker:            analytics.NewUsageTracker(),
		usageLedger:             analytics.NewUsageLedger(sessionDir, contentRoot),
		invokedSkills:           make(map[string]*skill.Meta),
		globalConfig:            globalCfg,
		projectConfig:           projectCfg,
		eventCh:                 make(chan Event, 256),
		eventWakeCh:             make(chan struct{}, 1),
		eventSpaceCh:            make(chan struct{}, 1),
		eventOverflowLimit:      defaultEventOverflowLimit,
		loopEventLimit:          defaultLoopEventLimit,
		outputCh:                make(chan AgentEvent, 512),
		sessionDir:              sessionDir,
		modelName:               modelName,
		runningModelRef:         modelName,
		instanceID:              NextInstanceID(identity.MainAgentID),
		mcpClientInfo:           mcpClientInfo,
		done:                    make(chan struct{}),
		stoppingCh:              make(chan struct{}),
		evidence:                evidenceCandidateTracker{seen: make(map[string]int)},
		cacheHitTracker:         promptcache.NewHitTracker(),
		contentRoot:             contentRoot,
		pathLocator:             pathLocator,
		subs:                    newSubAgentRegistry(),
		governor:                governor,
		waitingMainExpiry:       resolveWaitingMainExpiryPolicy(orchestrationCfg),
		sem:                     governor.runtimeSlots,
		fileTrack:               filelock.NewFileTracker(),
		fileBackups:             newFileBackupManager(sessionDir),
		runtimeStartedAt:        time.Now(),
		subAgentInbox:           newSubAgentInbox(),
		ownedSubAgentMailboxes:  make(map[string][]SubAgentMailboxMessage),
		ownedMailboxSpool:       make(map[string][]string),
		subAgentMailboxIDs:      make(map[string]struct{}),
		subAgentMailboxConsumed: make(map[string]struct{}),
		subAgentUrgentCounts:    make(map[string]int),
		recoveryOwner:           &recoveryOwnership{manager: recovery.NewRecoveryManager(sessionDir), dir: sessionDir},
		persist:                 newPersistencePump(256),
		cachedWorkDir:           workDir,
		gitStatusReady:          gitStatusReady,
		agentsMDReady:           make(chan struct{}),
		skillsReady:             make(chan struct{}),
		mcpReadyMu:              sync.Mutex{},
		mcpReady:                make(chan struct{}),
	}
	a.interaction = newInteractionBroker(a.stoppingCh)
	a.interaction.questionWaiting = a.questionWaiting.Load
	a.subPersists = newSubAgentPersistDebouncer(a.flushDirtySubPersists, a.SessionDir)
	a.walltime = newWalltimeRecorder(a.usageLedger, a.persist, a.stoppingCh)
	a.interaction.setSettledHook(func(target *walltimeTarget, d time.Duration) {
		if a.walltime != nil {
			a.walltime.recordTarget(target, analytics.WalltimePurposeUserWait, d)
		}
	})
	if llmClient != nil {
		llmClient.SetCandidateScorer(a.cacheAwareCandidateScore)
	}
	// Resolved lazily-read attachment payloads for the wire converter: restored
	// sessions keep blobs on disk, so requests load them on demand instead of
	// holding every attachment in memory from startup.
	a.installBinaryPartResolver()
	a.startPersistLoop()
	a.refreshSessionSummary()

	// Fetch git status asynchronously; callLLM waits for it before the first
	// LLM request of this process so every injected prefix carries the real
	// value from the start. It reads the live binding rather than the startup
	// directory: a session restored into a worktree installs that binding
	// while this goroutine may still be running, and both orders must end on
	// the restored checkout's branch.
	go func() {
		a.setCachedGitStatus(getGitStatus(a.workDir()))
		close(gitStatusReady)
	}()

	// Detect Python virtual environment synchronously (just os.Stat, cheap).
	a.setCachedVenvPath(detectVenvPath(workDir, contentRoot))

	// Wire Memory before building the system prompt so the stable prompt can
	// include the fixed Memory discipline when a MEMORY.md is present. The
	// background extraction worker starts here (project/process lifetime).
	if a.memoryMgr == nil {
		a.initMemory(contentRoot)
	}

	// Build and install the system prompt (git status is injected into the
	// first user message per request, not part of this prompt).
	a.refreshSystemPrompt()

	return a
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// SetConfirmFunc sets the callback used when a tool invocation requires user
// confirmation (permission action "ask"). This must be called before Run when
// the active ruleset can yield ActionAsk.
func (a *MainAgent) SetConfirmFunc(fn ConfirmFunc) {
	a.confirmFn = fn
}

// SetBusyPreparationHook installs a callback that runs before the main request
// surface is built for a new busy cycle. It is used by runtime resource
// managers to restore idle-unloaded dependencies before the next request.
func (a *MainAgent) SetBusyPreparationHook(fn func(context.Context) error) {
	a.busyPreparationMu.Lock()
	defer a.busyPreparationMu.Unlock()
	a.busyPreparationHook = fn
}

// SetSessionLock installs the ownership lock handle for the currently active session.
func (a *MainAgent) SetSessionLock(lock *recovery.SessionLock) {
	a.sessionLock = lock
	a.refreshSessionSummary()
}
