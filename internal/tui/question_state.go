package tui

import (
	"image"
	"time"

	"github.com/keakon/bubbles/v2/textarea"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

type QuestionRequest struct {
	Item tools.QuestionItem
	// Deadline is the absolute time after which the request is closed as
	// no_response (zero = wait indefinitely). The countdown is anchored to it;
	// the dialog never closes itself when it elapses, it waits for the matching
	// QuestionResolvedEvent.
	Deadline time.Time
	// AgentID is the asking agent's instance id ("main" for the main agent),
	// carried through from QuestionRequestEvent so the TUI can switch focus
	// to the agent whose question needs an answer.
	AgentID string
}

// questionDialog is one question request plus the broker ID that answers it: the
// TUI resolves through ResolveQuestion with that ID and drops the dialog on the
// matching QuestionResolvedEvent. The agent event handler installs it inline so
// a request and its close keep their event order instead of racing through the
// message loop.
type questionDialog struct {
	request   QuestionRequest
	requestID string
}

// questionTimeoutTickMsg is emitted every second while a question dialog is
// active and a deadline is configured. It drives the countdown display only.
type questionTimeoutTickMsg struct{ generation uint64 }

// questionState holds the transient state for the active question dialog.
type questionState struct {
	interacted, interacting, submitting bool
	request                             *QuestionRequest // full request (nil when inactive)
	requestID                           string           // broker request this dialog answers
	cursor                              int              // highlighted option (0-based)
	selected                            map[int]bool     // toggled option indices (multi-select)
	custom                              bool             // true when custom text input is focused
	input                               textarea.Model   // free-text input for custom answers / text-only Qs
	scrollOffset                        int
	followCursor                        bool
	bodyHeight                          int
	visibleBodyHeight                   int
	dialogRect                          image.Rectangle // rendered overlay bounds, for wheel hit-testing
	prevMode                            Mode            // mode to restore on close

	// deadline is the request's absolute close time from question_timeout.
	// The dialog only displays the countdown; the broker owns termination.
	deadline time.Time // zero value = no timeout

	renderCacheWidth    int
	renderCacheHeight   int
	renderCacheOffset   int
	renderCacheFollow   bool
	renderCacheTheme    string
	renderCacheReq      *QuestionRequest
	renderCacheCursor   int
	renderCacheSelected string
	renderCacheText     string
}

// questionTimeoutTick returns a tea.Cmd that sleeps for 1 second then
// delivers a questionTimeoutTickMsg (for countdown display).
func questionTimeoutTick(generation uint64) tea.Cmd {
	return tickCmd(time.Second, func(_ time.Time) tea.Msg {
		return questionTimeoutTickMsg{generation: generation}
	})
}
