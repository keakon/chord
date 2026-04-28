package tui

import (
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// BlockImagePart stores render metadata for an attached image preview.
type BlockImagePart struct {
	FileName  string
	ImagePath string
	MimeType  string
	Data      []byte
	Index     int

	RenderStartLine int
	RenderEndLine   int
	RenderCols      int
	RenderRows      int
}

// BlockType enumerates the kinds of visual elements rendered in the message viewport.
type BlockType int

const (
	BlockUser BlockType = iota
	BlockAssistant
	BlockThinking // extended thinking / reasoning (always expanded)
	BlockToolCall
	BlockToolResult
	BlockError
	BlockStatus
	BlockBoundaryMarker
	BlockCompactionSummary
)

// Block represents a single visual element in the conversation.
type Block struct {
	ID        int
	Type      BlockType
	Content   string // raw content (args JSON for tool calls, result text for tool results)
	Collapsed bool   // for tool blocks, default true
	ToolName  string // for tool blocks
	ToolID    string // tool call ID
	IsError   bool   // for tool results
	Streaming bool   // true while the block is still receiving deltas
	Focused   bool   // true when selected by mouse/keyboard
	AgentID   string // originating agent ID (for future TUI Agent View Isolation)

	// StartedAt is the wall clock when this block first became a live, visible
	// card in the current process. Zero after session restore from JSONL (no
	// fabricated times) or for blocks reconstructed from persisted history.
	StartedAt time.Time

	// SettledAt is the wall clock when this block reached a stable, final-visible
	// state in the current process. Zero after session restore from JSONL (no
	// fabricated times) or while still streaming / awaiting tool result / !shell.
	SettledAt time.Time

	// Tool result fields — set when the tool execution completes.
	// Stored on the same Block as the tool call so call+result render together.
	ResultContent      string                       // tool execution result text
	ResultStatus       agent.ToolResultStatus       // success, error, or cancelled
	ResultDone         bool                         // true once a terminal tool event has been received (even if result is empty)
	ToolExecutionState agent.ToolCallExecutionState // empty while speculative/unknown; queued or running once finalize dispatches execution state
	ToolProgress       *agent.ToolProgressSnapshot  // optional structured progress for running tools with real progress signals
	Audit              *message.ToolArgsAudit       // optional audit metadata for user-modified approved arguments
	PersistedDuration  time.Duration                // restored final elapsed time from durable tool result metadata

	// BackgroundObjectID links a durable status/result block to a background
	// service/job identifier so repeated finish notifications can update the same
	// visible card instead of appending duplicates.
	BackgroundObjectID string

	// LinkedAgentID is set for Delegate tool blocks when the result contains the
	// created subagent's instance ID (e.g. "agent-1"). Clicking the block
	// switches the view to that agent.
	LinkedAgentID string
	LinkedTaskID  string

	// DoneSummary is set when the linked SubAgent completes its task.
	// Displayed in the collapsed tool call view instead of the raw result.
	DoneSummary string

	// Diff holds a unified diff string for Write/Edit tool calls.
	// Not sent to the LLM; used only for TUI display.
	Diff string

	// ReadContentExpanded is true when the user has pressed space to show all Read result lines.
	// When false, Read shows at most maxReadDefaultLines (10) with a "[space to expand]" hint.
	ReadContentExpanded bool

	// ToolCallDetailExpanded: for generic tools (not Write/Edit/Read/Todo/Question), space toggles
	// between compact (first param + 10 result lines) and full (all params + full output).
	ToolCallDetailExpanded bool

	// diffHL is a lazily-initialised syntax highlighter shared by code-like
	// tool renderers (Read/Write/Edit). It caches lexer detection and rendered
	// snippets across renders of the same block.
	diffHL *codeHighlighter

	// toolArgsCache memoizes parsed JSON arguments for tool-call rendering.
	// It must be invalidated whenever ToolName or Content changes.
	toolArgsCacheToolName string
	toolArgsCacheContent  string
	toolArgsCacheKeys     []string
	toolArgsCacheVals     map[string]string

	// toolHeaderCache memoizes derived header/detail metadata built from parsed
	// tool args. It is invalidated alongside toolArgsCache.
	toolHeaderCacheToolName       string
	toolHeaderCacheContent        string
	toolHeaderCacheHeaderParams   string
	toolHeaderCacheHeaderParamsOK bool
	toolHeaderCacheHeaderMain     string
	toolHeaderCacheHeaderGray     string
	toolHeaderCacheHeaderPartsOK  bool
	toolHeaderCacheCollapsedMain  string
	toolHeaderCacheCollapsedGray  string
	toolHeaderCacheCollapsedOK    bool
	toolHeaderCacheCollapsedReady bool
	toolHeaderCacheParamLines     []string
	toolHeaderCacheParamLinesOK   bool

	// MsgIndex is the index of the corresponding message in the agent's message
	// list. Set for user blocks during transcript restore; -1 for dynamically
	// appended blocks or non-user blocks.
	MsgIndex int

	// FileRefs holds the @-mentioned file paths injected with this user message.
	// Used only for TUI display; not sent to the LLM separately (content is in Parts).
	FileRefs []string

	// ImageCount is the number of image attachments in this user message (> 0 when Parts contain images).
	ImageCount int

	// ImageParts holds metadata for each image attachment when available.
	ImageParts []BlockImagePart

	// UserLocalShell: merged USER + Bash-style !shell card (Type must be BlockUser).
	UserLocalShellCmd     string
	UserLocalShellPending bool
	UserLocalShellResult  string
	UserLocalShellFailed  bool

	// StatusTitle is the badge label for BlockStatus cards (e.g. "LOOP", "LOOP CONTINUE").
	StatusTitle string

	// LoopAnchor marks the user block whose prompt text started or resumed loop mode.
	// It is UI-only metadata and must not be treated as transcript content.
	LoopAnchor bool

	// ThinkingParts holds per-round thinking/reasoning text for assistant
	// blocks. Each entry is one LLM round's complete thinking content,
	// rendered dimmed above the main response text.
	ThinkingParts []string

	// ThinkingCollapsed controls whether thinking sections are collapsed
	// to maxCollapsedThinkingLines or shown in full.
	ThinkingCollapsed bool

	// ThinkingDuration records the elapsed time of the thinking phase,
	// displayed as a footer below thinking content.
	ThinkingDuration time.Duration

	// CompactionSummaryRaw stores the full persisted compaction message so the
	// TUI can switch between preview and full preserved-context views without
	// losing the expanded content.
	CompactionSummaryRaw string
	// compactionHL caches lexer detection and rendered snippets for fenced code
	// inside compaction summary markdown blocks.
	compactionHL *codeHighlighter
	// CompactionPreviewLines is the number of rendered markdown lines shown by
	// default when the compaction summary card is collapsed.
	CompactionPreviewLines int

	// Render caches - invalidated when content or width changes
	mdCache                      []string
	mdCacheWidth                 int
	mdCacheSyntheticPrefixWidths []int
	mdCacheSoftWrapContinuations []bool
	// streamSettled* caches the rendered markdown for the stable prefix of
	// in-flight streaming content. They survive append-only streaming updates so
	// unchanged settled prefixes can be reused across deltas.
	streamSettledRaw                   string
	streamSettledFrontier              int
	streamSettledWidth                 int
	streamSettledLines                 []string
	streamSettledSyntheticPrefixWidths []int
	streamSettledSoftWrapContinuations []bool
	streamSettledLineCount             int // lines from settled prefix in mdCache (rest are tail cheap-path)
	lineCache                          []string
	lineCacheWidth                     int
	lineCountCache                     int
	viewportCache                      []string
	viewportCacheWidth                 int
	renderSyntheticPrefixWidths        []int
	renderSoftWrapContinuations        []bool
	renderSyntheticPrefixWidthsW       int
	searchTextLower                    string
	searchTextReady                    bool

	spillRef        *BlockSpillRef
	spillStore      *ViewportSpillStore
	spillSummary    string
	spillLineCounts map[int]int
	spillCold       bool
	lastAccess      uint64
	spillRecover    func(blockID int) *Block
}
