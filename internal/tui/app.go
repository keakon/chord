package tui

import (
	"fmt"
	"image"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	tea "github.com/keakon/bubbletea/v2"
	uv "github.com/keakon/ultraviolet"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// ---------------------------------------------------------------------------
// Modes
// ---------------------------------------------------------------------------

// Mode represents the current interaction mode of the TUI.
type Mode int

const (
	ModeInsert               Mode = iota // text input is active
	ModeNormal                           // vim-style navigation
	ModeDirectory                        // Ctrl+T message directory overlay
	ModeConfirm                          // tool permission confirmation dialog
	ModeQuestion                         // Question tool multi-choice dialog
	ModeSearch                           // search input active
	ModeModelSelect                      // model pool selector overlay
	ModeMCPSelect                        // MCP server selector overlay (/mcp)
	ModeSessionSelect                    // session picker overlay (/resume)
	ModeSessionDeleteConfirm             // delete-session confirmation overlay from session picker
	ModeHandoffSelect                    // Handoff agent selector overlay
	ModeUsageStats                       // stats panel overlay ($, /stats)
	ModeErrorPanel                       // error panel overlay (ctrl+e)
	ModeHelp                             // full-screen help overlay
	ModeImageViewer                      // fullscreen image viewer overlay
	ModeContentViewer                    // fullscreen markdown content viewer overlay
	ModeRules                            // /rules overlay
)

// ---------------------------------------------------------------------------
// Custom messages
// ---------------------------------------------------------------------------

type sessionDeleteConfirmState struct {
	session  *agent.SessionSummary
	prevMode Mode

	renderCacheWidth  int
	renderCacheTheme  string
	renderCacheID     string
	renderCacheForked string
	renderCacheMsg    string
	renderCacheText   string
}

// focusInputMsg is sent once from Init to kick-start cursor blinking.
type focusInputMsg struct{}

// agentEventMsg wraps an event (or closure signal) from the agent channel.
type agentEventMsg struct {
	event  agent.AgentEvent
	closed bool
}

// agentEventBatchMsg carries a batch of agent events drained in one shot.
type agentEventBatchMsg []agentEventMsg

// keyPoolTickMsg triggers a sidebar refresh when key HTTP cooldown may have
// elapsed (see scheduleKeyPoolTick).
type keyPoolTickMsg struct {
	gen int
}

type statusBarTickMsg struct {
	generation uint64
}

const codexActiveRateLimitPollInterval = time.Minute

type toastTickMsg struct{ generation uint64 }

// clearPendingQuitMsg is sent after pendingQuitWindow (2s) to auto-clear the
// "press again to quit" hint. The generation field must match Model.quit.gen
// for the message to take effect, preventing stale timers from clearing newer
// pending quit state after the user pressed esc or another key.
type clearPendingQuitMsg struct{ generation uint64 }

type streamFlushTickMsg struct{ generation uint64 }
type scrollFlushTickMsg struct{ generation uint64 }

// Clipboard, local shell, and cached render helpers are in split files.

func shouldExpandToolResult(toolName string) bool {
	return tools.ShouldExpandResult(toolName)
}

func shouldTrackSidebarFileEdit(toolName string) bool {
	return tools.IsFileMutation(toolName)
}

// reconnectedMsg is sent when auto-reconnect succeeds and carries the new agent.
type reconnectedMsg struct {
	agent agent.AgentForTUI
}

// reconnectFailedMsg is sent when auto-reconnect exhausts all retries.
type reconnectFailedMsg struct{}

type sessionRestoredRebuildMsg struct {
	reason string
}

type statusBarCopyRegionState struct {
	value   string
	display string
	startX  int
	endX    int
}

type infoPanelSectionID string

const (
	infoPanelSectionLSP    infoPanelSectionID = "lsp"
	infoPanelSectionMCP    infoPanelSectionID = "mcp"
	infoPanelSectionGit    infoPanelSectionID = "git"
	infoPanelSectionTodos  infoPanelSectionID = "todos"
	infoPanelSectionFiles  infoPanelSectionID = "files"
	infoPanelSectionSkills infoPanelSectionID = "skills"
	infoPanelSectionAgents infoPanelSectionID = "agents"
)

type infoPanelSectionHitBox struct {
	section infoPanelSectionID
	agentID string
	startY  int
	endY    int
}

type statusPathState = statusBarCopyRegionState

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type kittyTerminalMetrics struct {
	CellWidthPx    int
	CellHeightPx   int
	WindowWidthPx  int
	WindowHeightPx int
	Valid          bool
}

// Model is the top-level Bubble Tea model for the Chord TUI.
type Model struct {
	agent                       agent.AgentForTUI
	viewport                    *Viewport
	input                       Input
	mode                        Mode
	width                       int
	height                      int
	expectedAgentClose          bool
	completedExpectedAgentClose bool
	theme                       Theme
	keyMap                      KeyMap
	ime                         imeState

	// Normal-mode Vim chord state
	chord               chordState
	chordTickGeneration uint64

	// Message-directory state (Ctrl+T)
	dirEntries []DirectoryEntry
	dirList    *OverlayList
	help       helpState
	usageStats usageStatsState

	// Streaming assistant block (nil when idle)
	currentAssistantBlock    *Block
	assistantBlockAppended   bool   // true once we've appended the block (avoids empty blocks)
	currentThinkingBlock     *Block // standalone streaming thinking block (nil when idle)
	thinkingBlockAppended    bool   // true once thinking block has been appended to viewport
	subAgentStreamStates     map[string]agentStreamState
	thinkingStreamMsgIndex   int
	thinkingStreamBlockIndex int
	nextBlockID              int
	lastDisplaySequence      map[string]int
	thinkingStartTime        time.Time // when the current thinking started
	streamFlushGeneration    uint64
	streamFlushScheduled     bool
	streamFlushDelay         time.Duration
	scrollFlushGeneration    uint64
	scrollFlushScheduled     bool
	pendingScrollDelta       int

	selectionState

	// Lifecycle
	quitting bool

	// Exit confirmation: first q or Ctrl+C sets these; second same key within 2s quits.
	quit quitState

	composerRuntimeState

	// Confirmation dialog channels and state
	confirmCh       chan ConfirmRequest
	confirmResultCh chan ConfirmResult
	confirm         confirmState

	// Question dialog channels and state
	questionCh chan QuestionRequest
	question   questionState

	// Search state
	search SearchModel

	// Model selector state
	modelSelect       modelSelectState
	pendingPoolSwitch pendingPoolSwitchState

	// MCP server selector state (/mcp)
	mcpSelect mcpSelectState

	// Session picker state (/resume)
	sessionSelect sessionSelectState

	// Delete confirmation state for a session selected in the session picker.
	sessionDeleteConfirm sessionDeleteConfirmState

	// /rules state
	rules rulesState

	// sessionSwitch tracks transient loading feedback for local session-control
	// operations such as /resume, /new, and fork. It is rendered in the status
	// bar until the switch completes or fails.
	sessionSwitch sessionSwitchState

	// Handoff agent selector state
	handoffSelect handoffSelectState

	// Fullscreen Markdown content viewer state
	contentViewer contentViewerState

	// Pending image attachments (shown above input box, sent with next message)
	attachments []Attachment
	// Session restore rebuilds are deferred; when a ForkSessionEvent in the same
	// event batch has already repopulated the composer, the deferred rebuild must
	// not wipe those restored attachments.
	preserveAttachmentsOnNextRebuild bool
	pendingSessionRestoreRebuild     bool

	completionState
	activityRuntimeState
	renderRuntimeState
	renderCacheState
	viewCacheState
	toolArgRenderState map[string]toolArgRenderState

	// terminalTitleView is the effective window title for the next View(). It is
	// rendered via Bubble Tea's View.WindowTitle, rather than writing directly to
	// stdout, to avoid interleaving OSC sequences with renderer output.
	terminalTitleView string

	// Terminal title state
	terminalTitleBase string // derived from first user message (no spinner)
	// terminalTitleBackgroundCompletedAgentID is set once when the focused agent
	// becomes idle while the terminal is blurred. It is cleared on focus and when
	// that same agent starts new work, so ordinary focus/tab/window switches do
	// not re-add the completion marker.
	terminalTitleBackgroundCompletedAgentID string
	agentHadEvent                           bool

	// keyPoolTickGen invalidates in-flight key-pool refresh ticks when agent events arrive.
	keyPoolTickGen int
	// reconnectFunc, when set, is called asynchronously after a connection drop.
	// It should return a new AgentForTUI on success, or an error after exhausting retries.
	reconnectFunc func() (agent.AgentForTUI, error)

	// Agent view isolation.
	// Empty string = main agent / show all blocks.
	focusedAgentID string

	// Multi-agent sidebar.
	sidebar Sidebar

	// Git status shown in the right info panel.
	gitStatus gitStatusState

	// Toast notifications
	toastQueue      []toastItem
	activeToast     *toastItem
	toastGeneration uint64

	// OSC terminal notifications (local TUI): emit when idle and terminal unfocused.
	terminalAppFocused           bool
	desktopNotificationsEnabled  bool
	oscNotifyOut                 io.Writer
	terminalNotificationProtocol terminalNotificationProtocol

	visibilityState
	cadenceProfiles cadenceProfiles
	startupRestoreState
	infoPanelCollapsedSections map[infoPanelSectionID]bool
	infoPanelHitBoxes          []infoPanelSectionHitBox
	infoPanelRenderCursorY     int
	infoPanelScrollOffset      int
	infoPanelContentHeight     int
	infoPanelViewportHeight    int

	// Layered drawing layout.
	layout tuiLayout
	// renderFrameGeneration increments for every drawFrame call. Diagnostics use
	// it to correlate captured buffers with rendered frames.
	renderFrameGeneration uint64
	// renderFrameDiag caches the last recorded render-frame diagnostic signature
	// so drawFrame records a new diagnostic only when a separator-relevant input
	// changes, keeping the steady-state render path free of diagnostic formatting.
	renderFrameDiag         renderFrameDiagSignature
	renderFrameDiagRecorded bool

	// rightPanelVisible tracks whether the right panel (info + agents) is shown.
	// Hysteresis: enabled at >=120 cols, disabled only when width drops below 116,
	// preventing flicker when the terminal sends transient intermediate sizes during
	// startup or tab switches.
	rightPanelVisible bool

	// Resize handling keeps layout stable for small shrink jitter while avoiding visible
	// blank space when the terminal grows. We always remember the latest observed
	// dimensions in pendingResizeW/H. Width/height growth is applied immediately so
	// the canvas keeps filling the terminal; small width/height shrink is debounced
	// for 40 ms so transient smaller sizes from tab switches do not yank the right
	// panel left. Large shrink (>= 6 cols or >= 4 rows) is applied immediately so
	// deliberate window drags do not feel laggy.
	pendingResizeW int
	pendingResizeH int
	resizeVersion  int

	runtimeCacheMgr          runtimeCacheManager
	runtimeCacheHandle       runtimeCacheSessionHandle
	runtimeCacheSession      string
	workingDir               string
	homeDir                  string
	instanceID               string
	statusPath               statusPathState
	imageCaps                TerminalImageCapabilities
	imageViewer              imageViewerState
	kittyMetrics             kittyTerminalMetrics
	kittyImageCache          map[int]struct{}
	kittyPlacementCache      map[int]struct{}
	lastImageProtocolAt      time.Time
	lastImageProtocolReason  string
	lastImageProtocolSummary string
	screenBuf                uv.ScreenBuffer
	// screenBlankLine caches one EmptyCell-filled row matching the current
	// screen buffer width, so Draw can clear the reused buffer with row copies
	// instead of per-cell loops.
	screenBlankLine uv.Line

	// Compaction background status (dual-lane status bar)
	compactionBgStatus compactionBackgroundStatus

	// tuiDiagnosticEvents keeps a small ring buffer of recent resize/focus/
	// block-update events so intermittent rendering issues can be dumped on
	// demand without spamming the hot path with synchronous logging.
	tuiDiagnosticEvents [maxTUIDiagnosticEvents]tuiDiagnosticEvent
	tuiDiagnosticNext   int
	tuiDiagnosticCount  int

	// agentErrors keeps a ring buffer of recent agent error events for the error panel.
	agentErrors      [maxAgentErrors]agentErrorRecord
	agentErrorsNext  int
	agentErrorsCount int

	errorPanel errorPanelState

	pendingLocalStatusCards []localStatusCard
}

type localStatusCard struct {
	title   string
	content string
}

type toolArgRenderState struct {
	lastBytes int
	lastAt    time.Time
}

// compactionBackgroundStatus tracks background compaction activity for dual-lane status bar
type compactionBackgroundStatus struct {
	Active     bool      // True when compaction is running or draft is ready
	StartedAt  time.Time // When compaction started
	Bytes      int64     // Optional dedicated compaction-progress bytes
	Events     int64     // Optional dedicated compaction-progress events
	Terminal   string    // Status: "" / "succeeded" / "failed" / "cancelled"
	TerminalAt time.Time // When status terminal was set (1-2s flush window)
}

// tuiLayout defines the positioning of UI elements for Draw(scr, area).
type tuiLayout struct {
	area        image.Rectangle
	main        image.Rectangle
	infoPanel   image.Rectangle
	attachments image.Rectangle // attachment bar (above input, when attachments present)
	queue       image.Rectangle // queued drafts bar (above attachments/input, when queue non-empty)
	input       image.Rectangle
	status      image.Rectangle
	toast       image.Rectangle
}

type toastItem struct {
	Message  string
	Level    string // info|warn|error
	Category string // same-category toasts in queue are merged; empty = no merge
}

// NewModel creates a fully initialised TUI model. Pass nil for agent to run
// without a backend (useful for tests).
func NewModel(a agent.AgentForTUI) Model {
	return NewModelWithSize(a, 80, 24)
}

// NewModelWithSize creates a fully initialised TUI model using the provided
// initial terminal dimensions. Callers should pass the real terminal size when
// it is already known before p.Run() so the first render does not use the 80×24
// placeholder layout.
func NewModelWithSize(a agent.AgentForTUI, width, height int) Model {
	initStarted := time.Now()
	theme := DefaultTheme()
	z := newZoneManager()
	wd, _ := os.Getwd()
	homeDir, _ := os.UserHomeDir()
	caps := detectTerminalImageCapabilitiesFromProcessEnv()
	setCurrentTerminalImageCapabilities(caps)
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	m := Model{
		agent:           a,
		viewport:        NewViewport(width, height),
		input:           NewInput(),
		mode:            ModeInsert,
		width:           width,
		height:          height,
		theme:           theme,
		keyMap:          DefaultKeyMap(),
		ime:             imeState{mu: &sync.Mutex{}},
		confirmCh:       make(chan ConfirmRequest, 1),
		confirmResultCh: make(chan ConfirmResult, 1),
		questionCh:      make(chan QuestionRequest, 1),
		sidebar:         NewSidebar(theme),
		composerRuntimeState: composerRuntimeState{
			agentComposerStates: make(map[string]agentComposerState),
		},
		activityRuntimeState: activityRuntimeState{
			activities:          make(map[string]agent.AgentActivityEvent),
			activityStartTime:   make(map[string]time.Time),
			activityLastChanged: make(map[string]time.Time),
			requestProgress:     make(map[string]requestProgressState),
			workStartedAt:       make(map[string]time.Time),
			turnBusyStartedAt:   make(map[string]time.Time),
			streamLastDeltaAt:   make(map[string]time.Time),
		},
		toolArgRenderState:  make(map[string]toolArgRenderState),
		lastDisplaySequence: make(map[string]int),
		selectionState: selectionState{
			focusedBlockID:  -1,
			zone:            z,
			selStartBlockID: -1,
			selEndBlockID:   -1,
			statusSession:   statusBarCopyRegionState{},
		},
		workingDir:                   wd,
		homeDir:                      homeDir,
		imageCaps:                    caps,
		kittyImageCache:              make(map[int]struct{}),
		kittyPlacementCache:          make(map[int]struct{}),
		statusPath:                   statusPathState{},
		terminalAppFocused:           true,
		terminalNotificationProtocol: detectTerminalNotificationProtocolFromProcessEnv(),
		visibilityState: visibilityState{
			displayState:     stateForeground,
			lastForegroundAt: time.Now(),
		},
		cadenceProfiles:            defaultCadenceProfiles(),
		infoPanelCollapsedSections: make(map[infoPanelSectionID]bool),
		runtimeCacheMgr:            newRuntimeCacheManager(),
		renderCacheState:           renderCacheState{statusBarAgentSnapshotDirty: true},
	}
	m.viewport.SetWorkingDir(wd)
	if a != nil {
		pending, sessionID := a.StartupResumeStatus()
		if pending {
			m.startupRestorePending = true
			m.beginSessionSwitch("resume", sessionID)
		} else {
			m.ensureRuntimeCacheSession(false)
		}
		if !pending && a.FocusedAgentID() == "" && len(a.GetMessages()) > 0 {
			m.setFocusedAgent("")
			m.rebuildViewportFromMessagesWithReason("model_init")
		}
	}

	m.refreshKittyTerminalMetrics()
	m.invalidateDrawCaches()

	ApplyTheme(theme)
	m.input.SetWidth(m.width - 2)
	// Default to main-only view: subagent blocks not shown in main view.
	m.viewport.SetFilter("main")
	m.updateRightPanelVisible()
	m.recalcViewportSize()
	if a != nil {
		log.Debugf("tui model init timing startup_restore_pending=%v blocks=%v width=%v height=%v total_ms=%v", m.startupRestorePending, len(m.viewport.blocks), m.width, m.height, time.Since(initStarted).Milliseconds())
	}
	return m
}

func (m *Model) syncWorkingDirFromAgent() {
	if m == nil {
		return
	}
	wd := strings.TrimSpace(m.workingDir)
	if m.agent != nil {
		if projectRoot := strings.TrimSpace(m.agent.ProjectRoot()); projectRoot != "" {
			wd = projectRoot
		}
	}
	if wd == m.workingDir {
		if m.viewport != nil {
			m.viewport.SetWorkingDir(wd)
		}
		return
	}
	m.workingDir = wd
	if m.viewport != nil {
		m.viewport.SetWorkingDir(wd)
	}
	m.invalidateStatusBarAgentSnapshot()
	m.invalidateDrawCaches()
}

func (m *Model) SetTheme(t Theme) {
	m.theme = t
	ApplyTheme(t)
	m.sidebar.theme = t
	m.invalidateDrawCaches()
}

func (m *Model) SetInstanceID(id string) {
	m.instanceID = strings.TrimSpace(id)
}

// SetDesktopNotification configures terminal idle notifications when the
// terminal loses focus. Pass nil out to disable writes (e.g. non-TTY). Local
// TUI only.
func (m *Model) SetDesktopNotification(enabled bool, out io.Writer) {
	m.desktopNotificationsEnabled = enabled
	m.oscNotifyOut = out
}

// SetKeyMap replaces the active key bindings. Call this after NewModel and
// before the Bubble Tea program starts (p.Run).
func (m *Model) SetKeyMap(km KeyMap) {
	m.keyMap = km
	m.input.SyncNewlineKeys(km.InsertNewline)
}

// ConfirmCh returns the send-only channel for submitting confirmation
// requests to the TUI. The agent (or its ConfirmFunc) writes to this channel.
func (m Model) ConfirmCh() chan<- ConfirmRequest { return m.confirmCh }

// ConfirmResultCh returns the receive-only channel for reading confirmation
// results from the TUI.
func (m Model) ConfirmResultCh() <-chan ConfirmResult { return m.confirmResultCh }

// ---------------------------------------------------------------------------
// tea.Model interface
// ---------------------------------------------------------------------------

// Init returns the initial set of commands.
func (m *Model) Init() tea.Cmd {
	m.ensureViewportCallbacks()
	cmds := []tea.Cmd{
		func() tea.Msg { return focusInputMsg{} },
		m.startAtMentionFileLoad(),
	}
	if cmd := m.startRuntimeCacheCleanup(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if m.agent != nil {
		cmds = append(cmds, waitForAgentEvent(m.agent.Events()))
		cmds = append(cmds, m.scheduleKeyPoolTick())
		cmds = append(cmds, m.scheduleStatusBarTick())
	}
	if cmd := m.requestGitStatusRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	cmds = append(cmds, waitForConfirmRequest(m.confirmCh))
	cmds = append(cmds, waitForQuestionRequest(m.questionCh))
	return tea.Batch(cmds...)
}

func streamFlushTick(generation uint64, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = foregroundCadence.contentFlushDelay
		if delay <= 0 {
			delay = 200 * time.Millisecond
		}
	}
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return streamFlushTickMsg{generation: generation}
	})
}

func scrollFlushTick(generation uint64, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = foregroundScrollFlushCadence
	}
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return scrollFlushTickMsg{generation: generation}
	})
}

// applyResizeMsg is fired 40 ms after the last WindowSizeMsg to debounce rapid
// resize events (e.g. terminal scrollbar appearing/disappearing during startup
// or after a tab switch).
type applyResizeMsg struct{ version int }

// Update is the central message dispatcher.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.ensureViewportCallbacks()
	switch msg := msg.(type) {

	// -- bootstrap cursor blink ------------------------------------------
	case focusInputMsg:
		cmd := m.input.Focus()
		return m, cmd

	case focusedContinueActionMsg:
		if m.agent == nil || m.focusedAgentID != msg.target.AgentID {
			m.stopActiveAnimationIfIdle()
			return m, nil
		}
		backend := m.agent
		targeted, hasTargeted := backend.(agent.TargetedConversationController)
		switch msg.action {
		case 0:
			m.stopActiveAnimationIfIdle()
			return m, nil
		case focusedContinueFromContext:
			return m, func() tea.Msg {
				if hasTargeted {
					targeted.ContinueFromContextForTarget(msg.target)
				} else {
					backend.ContinueFromContext()
				}
				return nil
			}
		case focusedContinueAfterRemovingLast:
			return m, func() tea.Msg {
				if hasTargeted {
					targeted.RemoveLastMessageForTarget(msg.target)
					targeted.ContinueFromContextForTarget(msg.target)
				} else {
					backend.RemoveLastMessage()
					backend.ContinueFromContext()
				}
				return nil
			}
		case focusedContinueWithDraft:
			draft := queuedDraft{Content: "continue", AgentID: msg.target.AgentID}
			m.finalizeAgentStream(msg.target.AgentID)
			staged := m.stageDraft(draft)
			send := func() tea.Msg {
				if hasTargeted {
					targeted.SendUserMessageToTarget(msg.target, "continue")
				} else {
					backend.SendUserMessage("continue")
				}
				return nil
			}
			return m, tea.Batch(staged, send)
		default:
			m.stopActiveAnimationIfIdle()
			return m, nil
		}

	case sessionRestoredRebuildMsg:
		m.syncWorkingDirFromAgent()
		shouldResetRuntimeCache := m.startupRestorePending || m.sessionSwitch.active()
		m.startupRestorePending = false
		if shouldResetRuntimeCache {
			m.lastDisplaySequence = make(map[string]int)
		}
		var (
			staleErr  error
			oldStore  *ViewportSpillStore
			oldHandle runtimeCacheSessionHandle
		)
		if shouldResetRuntimeCache {
			oldStore, oldHandle, staleErr = m.prepareRuntimeCacheSession(true)
		}
		m.rebuildViewportFromMessagesWithReason(msg.reason)
		m.finishRuntimeCacheSessionSwap(oldStore, oldHandle)
		m.clearSessionSwitch()
		// Update terminal title from the restored session's first user message.
		m.updateTerminalTitleFromRestoredSession()
		if staleErr != nil {
			log.Warnf("tui runtime cache reset failed error=%v", staleErr)
		}
		return m, tea.Batch(m.imageProtocolCmd(), m.scheduleStartupDeferredTranscriptPreheat(startupDeferredTranscriptPreheatDelay))

	// -- IME: got current IM before switching to English; save and switch to target
	case imeCurrentMsg:
		// Guard: if the user switched back to a mode that should not force English before
		// im-select returned, discard the result to avoid clobbering that mode's IME. Also
		// ignore stale switch completions once a newer IME apply request has already been queued.
		if !modeNeedsEnglishIME(m.mode) || !m.imeSwitchAllowed() {
			return m, nil
		}
		m.ime.mu.Lock()
		stale := msg.seq != m.ime.seq
		hasPending := m.ime.pending && strings.TrimSpace(m.ime.pendingTarget) != ""
		pendingTarget := m.ime.pendingTarget
		m.ime.mu.Unlock()
		if stale {
			if hasPending {
				m.queueIMEApply(pendingTarget)
			}
			return m, nil
		}
		m.ime.beforeNormal = msg.current
		if m.ime.switchTarget != "" {
			m.queueIMEApply(m.ime.switchTarget)
		}
		return m, nil

	case tea.EnvMsg:
		env := mapFromEnvMsg(msg)
		caps := detectTerminalImageCapabilitiesFromMap(env)
		setCurrentTerminalImageCapabilities(caps)
		m.imageCaps = caps
		m.terminalNotificationProtocol = detectTerminalNotificationProtocolFromMap(env)
		m.cadenceProfiles = detectCadenceProfilesFromMap(env)
		return m, m.imageProtocolCmd()

	// -- attachment loaded ----------------------------------------------
	case diagnosticsBundleMsg:
		if msg.err != nil {
			m.recordTUIDiagnostic("diagnostics-bundle-error", "%v", msg.err)
			return m, m.enqueueToast(fmt.Sprintf("Diagnostics export failed: %v", msg.err), "error")
		}
		m.recordTUIDiagnostic("diagnostics-bundle-written", "%s", msg.path)
		m.appendDiagnosticsStatusCard(formatDiagnosticsStatusCard(msg.path))
		return m, tea.Batch(
			m.enqueueToast("Diagnostics bundle exported", "info"),
		)

	case attachmentReadyMsg:
		return m, m.handleAttachmentReadyMsg(msg)

	case clipboardAttachmentReadyMsg:
		return m, m.handleClipboardAttachmentReady(msg)

	case openImageResultMsg:
		if msg.err != nil {
			return m, m.enqueueToast(msg.err.Error(), "warn")
		}
		return m, nil

	case openImageViewerMsg:
		m.clearMouseSelection()
		if msg.blockID >= 0 {
			if blk := m.viewport.GetFocusedBlock(msg.blockID); blk != nil && isSelectableBlockType(blk.Type) {
				m.focusedBlockID = msg.blockID
			} else {
				m.focusedBlockID = -1
			}
			m.refreshBlockFocus()
		}
		m.openImageViewer(msg.blockID, msg.imageIndex)
		return m, m.imageProtocolCmd()

	// -- clipboard text paste ---------------------------------------------
	case clipboardTextMsg:
		if m.mode == ModeConfirm && m.confirm.editing {
			m.confirm.editInput.InsertString(string(msg))
			m.recalcViewportSize()
			return m, nil
		}
		if m.mode == ModeConfirm && m.confirm.denyingWithReason {
			m.confirm.denyReasonInput.InsertString(string(msg))
			m.recalcViewportSize()
			return m, nil
		}
		if m.mode == ModeHandoffSelect && m.handoffSelect.denyingWithReason {
			m.handoffSelect.denyReasonInput.InsertString(string(msg))
			m.recalcViewportSize()
			return m, nil
		}
		m.input.ClearSelection()
		if m.input.InsertLargePaste(string(msg)) {
			m.input.syncHeight()
			cmd := m.syncAtMentionIfOpen()
			m.recalcViewportSize()
			return m, cmd
		}
		return m, m.insertComposerText(string(msg))

	case atMentionFilesLoadedMsg:
		m.atMentionFiles = msg.files
		m.atMentionLoaded = true
		m.atMentionLoadedAt = time.Now()
		m.atMentionLoading = false
		return m, m.syncAtMentionIfOpen()

	// -- terminal resize -------------------------------------------------
	case tea.BlurMsg:
		return m, m.handleBlurUpdate()

	case chordTimeoutMsg:
		if msg.generation != m.chordTickGeneration {
			return m, nil
		}
		m.clearChordState()
		return m, nil

	case tea.FocusMsg:
		return m, m.handleFocusUpdate()

	case tea.WindowSizeMsg:
		return m, m.handleWindowSizeUpdate(msg)

	case applyResizeMsg:
		return m, m.handleApplyResize(msg)

	// -- agent events ----------------------------------------------------
	case agentEventBatchMsg:
		var cmds []tea.Cmd
		needKeyPoolTick := false
		for _, item := range msg {
			cmds = append(cmds, m.handleAgentEvent(item))
			if m.displayState == stateBackground {
				if idleCmd := m.updateBackgroundIdleSweepState(); idleCmd != nil {
					cmds = append(cmds, idleCmd)
				}
			}
			if agentEventMayChangeKeyPool(item) {
				needKeyPoolTick = true
			}
		}
		// Re-subscribe once after processing the whole batch to avoid spawning
		// N goroutines for N events in a single batch.
		if m.agent != nil {
			cmds = append(cmds, waitForAgentEvent(m.agent.Events()))
		}
		if needKeyPoolTick {
			m.keyPoolTickGen++
			cmds = append(cmds, m.scheduleKeyPoolTick())
		}
		cmds = append(cmds, m.restartStatusBarTick())
		if !m.renderFreezeActive && m.currentCadence().contentFlushDelay > 0 {
			cmds = append(cmds, m.scheduleStreamFlush(0))
		}
		return m, tea.Batch(cmds...)

	case keyPoolTickMsg:
		if msg.gen != m.keyPoolTickGen {
			return m, nil
		}
		// If a Codex rate-limit reset timestamp has been reached, trigger a usage poll
		// so the sidebar can move from an expired window to the new one quickly. While
		// the agent is active, also poll at most once per minute in case the Responses
		// stream stops emitting inline rate_limits events after a quota window recovers.
		type codexUsagePoller interface {
			WakeCodexRateLimitPolling()
		}
		if p, ok := m.agent.(codexUsagePoller); ok && m.agent != nil {
			now := time.Now()
			wake := false
			if snap := m.agent.CurrentRateLimitSnapshot(); snap != nil {
				expiredPrimary := snap.Primary != nil && !snap.Primary.ResetsAt.IsZero() && !snap.Primary.ResetsAt.After(now)
				expiredSecondary := snap.Secondary != nil && !snap.Secondary.ResetsAt.IsZero() && !snap.Secondary.ResetsAt.After(now)
				staleSnapshot := !snap.CapturedAt.IsZero() && now.Sub(snap.CapturedAt) >= codexActiveRateLimitPollInterval
				wake = expiredPrimary || expiredSecondary || (staleSnapshot && m.hasActiveAgentActivity())
			}
			if wake {
				p.WakeCodexRateLimitPolling()
			}
		}

		m.invalidateUsageStatsCache()
		m.refreshSidebar()
		return m, m.scheduleKeyPoolTick()

	case statusBarTickMsg:
		if msg.generation != m.statusBarTickGeneration {
			return m, nil
		}
		m.statusBarTickScheduled = false
		return m, m.scheduleStatusBarTick()

	case gitStatusRefreshedMsg:
		return m, m.handleGitStatusRefreshed(msg)

	case gitStatusTickMsg:
		return m, m.handleGitStatusTick(msg)

	case reconnectedMsg:
		return m, m.handleReconnected(msg)

	case reconnectFailedMsg:
		return m, m.handleReconnectFailed()

	case terminalTitleTickMsg:
		return m, m.handleTerminalTitleTick(msg)

	case animTickMsg:
		return m, m.handleAnimTick(msg)

	case idleSweepTickMsg:
		return m, m.handleIdleSweepTick(msg)

	case startupDeferredPreheatTickMsg:
		return m, m.handleStartupDeferredTranscriptPreheat(msg)

	case streamFlushTickMsg:
		return m, m.handleStreamFlushTick(msg)

	case scrollFlushTickMsg:
		return m, m.consumeScrollFlush(msg)

	case shellBangResultMsg:
		return m, m.handleShellBangResult(msg)

	case toastTickMsg:
		if msg.generation != m.toastGeneration {
			return m, nil
		}
		return m, m.handleToastTick()

	case clipboardWriteResultMsg:
		return m, m.handleClipboardWriteResult(msg)

	case sessionSummariesLoadedMsg:
		return m, m.handleSessionSummariesLoaded(msg)

	case sessionSummaryDetailsLoadedMsg:
		return m, m.handleSessionSummaryDetailsLoaded(msg)

	case projectUsageLoadedMsg:
		return m, m.handleProjectUsageLoaded(msg)

	case clearPendingQuitMsg:
		if msg.generation == m.quit.gen {
			m.clearPendingQuit()
		}
		return m, nil

	// -- confirmation request from agent ---------------------------------
	case confirmRequestMsg:
		return m, m.handleConfirmRequest(msg)

	case confirmTimeoutTickMsg:
		return m, m.handleConfirmTimeoutTick()

	// -- question request from Question tool ----------------------------
	case questionRequestMsg:
		return m, m.handleQuestionRequest(msg)

	case modelSwitchResultMsg:
		return m, m.handleModelSwitchResult(msg)

	// -- question timeout tick ------------------------------------------
	case questionTimeoutTickMsg:
		return m, m.handleQuestionTimeoutTick()

	// -- mouse (v2: MouseMsg has Mouse() for X,Y,Button; use type switch for action) --
	case tea.MouseMsg:
		return m, m.handleMouseMsg(msg)

	case tea.KeyReleaseMsg:
		return m, nil
	case tea.KeyMsg:
		return m, m.handleKeyMsg(msg)
	}

	// Route non-key messages (cursor blink, paste etc.) to the active input.
	return m, m.handleNonKeyInputMsg(msg)
}

// ---------------------------------------------------------------------------
// Animation tick (replaces Bubble Tea spinner for status-bar + tool-call anim)
// ---------------------------------------------------------------------------

type animTickSource uint8

const (
	animTickSourceVisual animTickSource = iota
	animTickSourceHousekeeping
)

type animTickMsg struct {
	generation uint64
	source     animTickSource
}

type requestProgressState struct {
	RawBytes      int64
	RawEvents     int64
	VisibleBytes  int64
	VisibleEvents int64
	BaseBytes     int64
	BaseEvents    int64
	LastUpdatedAt time.Time
	Done          bool
}

// ---------------------------------------------------------------------------
// View helpers
// ---------------------------------------------------------------------------
