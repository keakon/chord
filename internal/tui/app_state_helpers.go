package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

type activityRuntimeState struct {
	activities          map[string]agent.AgentActivityEvent
	activityStartTime   map[string]time.Time
	activityLastChanged map[string]time.Time
	requestProgress     map[string]requestProgressState
	workStartedAt       map[string]time.Time
	turnBusyStartedAt   map[string]time.Time
	streamLastDeltaAt   map[string]time.Time
}

type renderCacheState struct {
	statusBarAgentSnapshotDirty bool
}

type startupRestoreState struct {
	startupRestorePending bool
}

type renderRuntimeState struct {
	animRunning                      bool
	animTickGeneration               uint64
	activitySpinnerFrameIndex        int
	statusBarTickGeneration          uint64
	statusBarTickScheduled           bool
	terminalTitleTickRunning         bool
	terminalTitleTickGeneration      uint64
	terminalTitleRequestBlinkOff     bool
	terminalTitleRequestSeen         bool
	startupDeferredTranscript        *startupDeferredTranscriptState
	startupDeferredPreheatGeneration uint64
}

type selectionState struct {
	focusedBlockID int // ID of the block selected by mouse/keyboard
	zone           *ZoneManager

	// In-app mouse text selection (drag to select, release to copy)
	mouseDown              bool
	selStartBlockID        int
	selStartLine           int
	selStartCol            int
	selEndBlockID          int
	selEndLine             int
	selEndCol              int
	selEndInclusiveForCopy bool
	inputMouseDown         bool
	inputLastClickTime     time.Time
	inputLastClickY        int
	inputLastClickX        int
	inputClickCount        int

	// Status bar copy targets (working directory / session ID).
	statusPathLastClickTime time.Time
	statusPathLastClickX    int
	statusPathLastClickY    int
	statusPathClickCount    int
	statusSession           statusBarCopyRegionState

	// Double/triple click for word/line selection
	lastClickTime time.Time
	lastClickY    int
	lastClickX    int
	clickCount    int
}

type completionState struct {
	// File reference completion ("@path" in composer)
	atMentionOpen       bool
	atMentionLine       int
	atMentionTriggerCol int
	atMentionQuery      string
	atMentionLoaded     bool
	atMentionLoading    bool
	atMentionLoadedAt   time.Time
	atMentionFiles      []string
	atMentionList       *OverlayList

	// Slash command completion (when input starts with "/")
	slashCompleteSelected int            // index into current completion list
	customCommands        []slashCommand // extra commands injected from config
}

type visibilityState struct {
	// displayState tracks whether the terminal is considered focused (foreground)
	// or blurred (background). This is best-effort only: driven by BlurMsg/FocusMsg.
	displayState displayState
	// lastForegroundAt records when we last received a FocusMsg.
	lastForegroundAt time.Time
	// lastBackgroundAt records when we last received a BlurMsg.
	lastBackgroundAt time.Time
	// lastSweepAt records when the last idle sweep ran.
	lastSweepAt time.Time
	// idleSweepScheduled is set when an idle sweep tick generation is live.
	idleSweepScheduled bool
	// idleSweepGeneration guards against stale idle sweep tick messages.
	idleSweepGeneration uint64
	// backgroundIdleSince tracks when the currently focused view last became idle
	// while the terminal was in background mode. Zero means no active idle window.
	backgroundIdleSince time.Time
	// deferredResumeTailOnFocus records whether the focused deferred transcript was
	// logically pinned to the tail when the terminal blurred, so focus recovery can
	// restore the true tail window instead of leaving the user at a stale page bottom.
	deferredResumeTailOnFocus bool
}

// slashRenderCache memoizes the rendered slash-completion popup so identical
// composer state reuses the previous output. All zero values mean "miss".
type slashRenderCache struct {
	width int
	theme string
	value string
	sel   int
	text  string
}

// viewCacheState aggregates render-layer caches that the draw loop invalidates
// in bulk. Every field MUST satisfy "zero value == invalid cache" so
// invalidateDrawCaches can simply re-zero the struct (see app_cached_render.go).
// The single exception is cachedMainSearchBlockIndex (-1 == no search), which
// invalidateDrawCaches re-applies after the zero-out.
type viewCacheState struct {
	streamRenderDeferred               bool
	streamRenderForceView              bool
	streamRenderDeferNext              bool
	cachedFullView                     tea.View
	cachedFullViewValid                bool
	renderFreezeActive                 bool
	renderFreezeReason                 string
	renderFreezeEnteredAt              time.Time
	cachedFrozenView                   tea.View
	cachedFrozenViewValid              bool
	cachedMainSpinnerFrame             string
	cachedMainSearchBlockIndex         int
	cachedMainSelActive                bool
	cachedMainSel                      SelectionRange
	cachedMainKey                      string
	cachedMainRender                   cachedRenderable
	cachedInputMode                    Mode
	cachedInputWidth                   int
	cachedInputHeight                  int
	cachedInputSuppressed              bool
	cachedInputSelectionAlive          bool
	cachedInputFocused                 bool
	cachedInputBangMode                bool
	cachedInputValue                   string
	cachedInputLine                    int
	cachedInputColumn                  int
	cachedInputScrollY                 int
	cachedInputSelection               inputSelection
	cachedInputKey                     string
	cachedInputAnimKey                 string
	cachedInputRender                  cachedRenderable
	cachedInputCursor                  tea.Cursor
	cachedInputCursorOK                bool
	cachedStatusKey                    string
	cachedStatusRender                 cachedRenderable
	cachedStatusBarModeKey             string
	cachedStatusBarModePill            string
	cachedStatusBarViewingKey          string
	cachedStatusBarViewingPill         string
	cachedStatusBarPillsKey            string
	cachedStatusBarLeftSide            string
	cachedStatusBarLeftW               int
	cachedStatusBarRightKey            string
	cachedStatusBarRightSide           string
	cachedStatusBarRightWidth          int
	cachedStatusBarRightStart          int
	cachedStatusBarPathValue           string
	cachedStatusBarPathShown           string
	cachedStatusBarActivityKey         string
	cachedStatusBarActivityText        string
	cachedStatusBarActivityWidth       int
	cachedSepWidth                     int
	cachedSepTheme                     string
	cachedSepBusy                      bool
	cachedSepInsert                    bool
	cachedSepFrame                     int64
	cachedSepResult                    string
	cachedQueuePresent                 bool
	cachedQueueWidth                   int
	cachedQueueHeight                  int
	cachedQueueKey                     string
	cachedQueueRender                  cachedRenderable
	cachedAttachmentsPresent           bool
	cachedAttachKey                    string
	cachedAttachRender                 cachedRenderable
	cachedToastKey                     string
	cachedToastRender                  cachedRenderable
	cachedHelpRender                   cachedRenderable
	cachedDirRender                    cachedRenderable
	cachedInfoPanelW                   int
	cachedInfoPanelH                   int
	cachedInfoPanelFP                  string
	cachedInfoPanelOut                 string
	statusBarAgentSnapshot             statusBarAgentSnapshot
	statusBarSyntheticConnectingLogKey string
	cachedStatusBarSessionValue        string
	cachedStatusBarSessionShown        string
	cachedModelPillRef                 string
	cachedModelPillVariant             string
	cachedModelPillEffW                int
	cachedModelPillLeftW               int
	cachedModelPill                    string
	slashCache                         slashRenderCache
}
