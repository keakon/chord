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
	statusBarTickGeneration          uint64
	statusBarTickScheduled           bool
	terminalTitleTickRunning         bool
	terminalTitleTickGeneration      uint64
	terminalTitleRequestBlinkOff     bool
	startupDeferredTranscript        *startupDeferredTranscriptState
	startupDeferredPreheatGeneration uint64
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
