// Package agent defines the interface that the TUI uses to interact with
// a local MainAgent.

package agent

import (
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

// MessageSender covers user-message submission, queued drafts, and turn
// continuation. Implemented by [MainAgent].
type MessageSender interface {
	SendUserMessage(content string)
	// SendUserMessageWithParts sends a user message that may include images.
	SendUserMessageWithParts(parts []message.ContentPart)
	// AppendContextMessage appends a user message to the model context without
	// invoking the LLM. Content may differ from Parts when persistence needs a
	// machine-readable form but the live context should stay human-readable.
	AppendContextMessage(msg message.Message)
	CancelCurrentTurn() bool

	// QueuePendingUserDraft mirrors a busy local draft into the agent's pending
	// queue so it can be consumed later without showing in the transcript early.
	QueuePendingUserDraft(draftID string, parts []message.ContentPart) bool
	// UpdatePendingUserDraft replaces a queued draft before it is consumed.
	UpdatePendingUserDraft(draftID string, parts []message.ContentPart) bool
	// RemovePendingUserDraft removes a queued draft before it is consumed.
	RemovePendingUserDraft(draftID string) bool

	// ContinueFromContext re-runs the LLM using the existing context without
	// appending a new user message. Routes to focused SubAgent if one is active.
	ContinueFromContext()
	// RemoveLastMessage removes the last message from context and rewrites
	// persistence. Used before ContinueFromContext when last message is a
	// thinking-only assistant block that was interrupted.
	RemoveLastMessage()
}

// ConversationTarget identifies one conversation independently of the mutable
// TUI focus. TaskID keeps a SubAgent target stable when its parked runtime is
// rehydrated under a new instance ID.
type ConversationTarget struct {
	AgentID string
	TaskID  string
}

// TargetedConversationController performs deferred TUI actions against the
// conversation captured when the action was created, not whichever agent is
// focused when an asynchronous command eventually runs.
type TargetedConversationController interface {
	GetMessagesForTarget(target ConversationTarget) []message.Message
	SendUserMessageToTarget(target ConversationTarget, content string)
	ContinueFromContextForTarget(target ConversationTarget)
	RemoveLastMessageForTarget(target ConversationTarget)
}

// PromptResolver delivers user responses for confirm/question dialogs back to
// the agent's pending interaction flow.
type PromptResolver interface {
	// ResolveConfirm sends the user's confirmation response back to the pending
	// confirm flow.
	ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string)
	// ResolveQuestion sends the user's question response back to the pending
	// question flow. reason is answered or declined. It returns the request's
	// terminal reason and whether the broker accepted the response as that
	// state: a response that lost to the deadline comes back as no_response,
	// and a duplicate or unknown request as ("", false).
	ResolveQuestion(answers []string, reason string, requestID string) (string, bool)
}

// HandoffResolver delivers the user's plan-execution decision back to the
// pending handoff flow. The decision settles the handoff user-wait and emits
// the deferred handoff tool result; the runtime then executes the plan
// (approve), continues from context (deny), or stays idle (cancel).
type HandoffResolver interface {
	ResolveHandoff(requestID, action, agentName, denyReason string)
}

// ModelSelector exposes model identity for the status bar and model pool controls.
type ModelSelector interface {
	ProviderModelRef() string
	RunningModelRef() string
	RunningVariant() string
	// CurrentPoolName returns the effective pool name for the agent currently shown
	// in the TUI (focused SubAgent if any, else current main role), or "" if no pool
	// policy is configured.
	CurrentPoolName() string
	// PoolNames returns the pool names for the agent currently shown in the TUI.
	PoolNames() []string
	// MainModelPoolName returns the effective pool name for the current main
	// role regardless of focused SubAgent state.
	MainModelPoolName() string
	// MainModelPoolNames returns the pool names for the current main role regardless
	// of focused SubAgent state.
	MainModelPoolNames() []string
	// AgentOverridePoolName returns the explicit override for the named agent, if any.
	AgentOverridePoolName(agentName string) (string, bool)
	// SetCurrentModelPool sets the current main model pool.
	SetCurrentModelPool(pool string) error
	// SetAgentModelPool sets the named agent's pool.
	SetAgentModelPool(agentName, pool string) error
}

// FocusedModelState is one consistent view of the model configuration for the
// agent currently shown by the TUI, including restored parked SubAgents.
type FocusedModelState struct {
	SelectedRef string
	RunningRef  string
	Variant     string
	PoolName    string
	PoolNames   []string
}

// FocusedModelStateProvider avoids assembling model state through several
// independently routed getters that can disagree during session restore.
type FocusedModelStateProvider interface {
	FocusedModelState() FocusedModelState
}

// SessionController exposes session lifecycle controls (resume, fork, delete,
// export).
type SessionController interface {
	ListSessionSummaries() ([]SessionSummary, error)
	GetSessionSummary() *SessionSummary
	DeleteSession(sessionID string) error
	ExportSession(format, path string)
	ResumeSession()
	ResumeSessionID(sessionID string)
	NewSession()
	// ForkSession creates a new session branching from the message at msgIndex.
	// The message at msgIndex becomes the draft loaded into the composer.
	ForkSession(msgIndex int)
}

// SubAgentInspector lets the TUI list, focus, and follow subagents.
type SubAgentInspector interface {
	GetSubAgents() []SubAgentInfo
	SwitchFocus(agentID string)
	// FocusedAgentID returns the instance ID of the focused SubAgent, or "" when
	// the main agent is focused.
	FocusedAgentID() string
	// FocusedAgentName returns the agent definition name of the focused SubAgent,
	// or "" when the main agent is focused.
	FocusedAgentName() string
}

// LoopController exposes the post-assistant loop-mode runtime state.
type LoopController interface {
	// LoopKeepsMainBusy reports whether the local MainAgent remains in a
	// non-terminal loop state even if no turn is currently active.
	LoopKeepsMainBusy() bool
	// CurrentLoopState returns the current loop-controller state for the main
	// agent, or empty string when loop mode is disabled / unsupported.
	CurrentLoopState() LoopState
	CurrentLoopTarget() string
	CurrentLoopIteration() int
	CurrentLoopMaxIterations() int
	EnableLoopMode(target string)
	DisableLoopMode()
	CanUseLoopMode() bool
}

type YoloController interface {
	YoloEnabled() bool
}

// MemoryStatusReporter exposes the effective automatic memory-extraction
// setting for the main agent (status bar MEMORY pill), plus whether memory
// setup or the last commit failed permanently.
type MemoryStatusReporter interface {
	MemoryEnabled() bool
	MemoryDegraded() bool
}

// ServiceTierReporter exposes effective runtime service-tier state for command completion and status UI.
type ServiceTierReporter interface {
	ServiceTier() config.ServiceTier
	EffectiveServiceTier() config.ServiceTier
	SupportedServiceTiers() []config.ServiceTier
}

// RoleController exposes role/handoff lifecycle for the active agent.
type RoleController interface {
	// SwitchRole requests the agent to switch its active role.
	// The new role is broadcast as RoleChangedEvent on success; an error is
	// returned without emitting the event when the role is unknown, exists
	// only as a SubAgent definition, or the switch fails otherwise.
	SwitchRole(role string) error
	// AvailableRoles returns the ordered list of role names the user can cycle
	// through with the Tab key in the main agent view.
	AvailableRoles() []string
	CurrentRole() string
	// AvailableAgents returns the names of agent roles available for Handoff.
	AvailableAgents() []string
}

// UsageReporter aggregates token usage and context-window stats for status,
// sidebar, and stats overlay rendering.
type UsageReporter interface {
	GetTokenUsage() message.TokenUsage
	// GetUsageStats returns session-wide totals (e.g. $ /stats Session overview and per-agent table).
	GetUsageStats() analytics.SessionStats
	// GetSidebarUsageStats returns token/cost totals for the focused agent only, aligned with
	// GetContextStats and GetTokenUsage for the right info panel and footer pills.
	GetSidebarUsageStats() analytics.SessionStats
	// GetSidebarWalltimeStats returns wall-clock time distribution (model /
	// tool / cooldown / user wait) for the focused agent only, for the TIME
	// info panel section. All buckets are zero when nothing has been recorded.
	GetSidebarWalltimeStats() analytics.WalltimeStats
	// GetContextStats returns current input-context usage and usable input budget for the focused agent.
	// current is the usage-only reading (observed baseline, frozen estimate, or 0
	// when unknown); limit is the usable input budget (0 if unknown).
	GetContextStats() (current, limit int)
	// GetContextUsageState reports the observation state behind GetContextStats:
	// observed, estimated (frozen), or unknown. Unknown renders as 0 and never
	// triggers compaction; the frozen estimate must be marked as approximate,
	// because it is computed instead of provider-observed.
	GetContextUsageState() ctxmgr.ContextUsageState
	// ContextPressureLinesForModelRef returns the context-pressure reminder and
	// auto-compaction lines that a model at modelRef would manage its context
	// with, as usage ratios in the same frame as GetContextStats (current /
	// usable input budget). The mapping is pure configuration (per-model →
	// global → derived), independent of what the agent is currently running.
	// The TUI resolves the displayed model itself — the running model while
	// busy, the next-request model otherwise, so a pending model switch
	// re-colors the context display immediately — and queries per that ref.
	// Both lines are 0 when the ref is unknown/empty or automatic compaction
	// is disabled (threshold 0); agents that never manage context with
	// usage-driven lines (focused SubAgent, parked target) must not be queried
	// through this and keep their fixed fallback lines instead.
	ContextPressureLinesForModelRef(modelRef string) (reminder, threshold float64)
	// GetContextMessageCount returns the number of messages in the focused agent's context (for sidebar). -1 if unknown.
	GetContextMessageCount() int
	// GetContextBytes returns request payload bytes for the focused agent.
	GetContextBytes() int
	// GetContextReductionStats returns request-level prompt trimming effect for the focused agent.
	GetContextReductionStats() ContextReductionStats
}

// KeyHealthReporter exposes provider key/rate-limit/proxy state for the right
// info panel.
type KeyHealthReporter interface {
	// KeyStats returns (available, total) API keys for the focused agent's provider.
	KeyStats() (available, total int)
	// CurrentRateLimitSnapshot returns the latest rate-limit snapshot for the active key, or nil.
	CurrentRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot
	ProxyInUseForRef(ref string) bool
}

// CompactionController exposes durable compaction state for the status bar.
type CompactionController interface {
	// IsCompactionRunning reports whether a compaction goroutine is in flight.
	IsCompactionRunning() bool
	// CancelCompaction requests cancellation of active compaction work. Returns
	// true when there was work to cancel.
	CancelCompaction() bool
}

// PlanExecutor triggers plan-execution workflows.
type PlanExecutor interface {
	// ExecutePlan triggers execution of a plan with the specified target agent.
	// agentName may be empty (defaults to "builder").
	ExecutePlan(planPath, agentName string)
}

// WorkDirSnapshot is the display-relevant view of an agent's active checkout:
// the effective working directory, the chord-managed worktree identity working
// there (empty when the directory is not a managed checkout), and the
// generation the binding was published with.
type WorkDirSnapshot struct {
	Path       string
	WorktreeID string
	Generation uint64
}

// AgentForTUI is the full interface required by the local TUI. New code that
// consumes only a slice of this surface should target the smaller sub-interfaces
// (MessageSender, ModelSelector, …) instead.
type AgentForTUI interface {
	Events() <-chan AgentEvent
	GetMessages() []message.Message
	StartupResumeStatus() (pending bool, sessionID string)
	// ContentRoot returns the root the project's content and machine state are
	// anchored to (the main worktree root inside a linked worktree).
	ContentRoot() string
	// WorkDir returns the checkout the agent's tools and shell commands run in.
	WorkDir() string
	// WorkDirSnapshot returns that checkout together with the worktree identity
	// working there and the generation the binding was published with. Callers
	// that render or compare the checkout must prefer it over combining
	// WorkDir() with anything else: separate reads can straddle a switch.
	WorkDirSnapshot() WorkDirSnapshot
	// InvokedSkills returns skills explicitly loaded via the Skill tool in the current session.
	InvokedSkills() []*skill.Meta
	// GetTodos returns the current todo list for sidebar display.
	GetTodos() []tools.TodoItem

	MessageSender
	PromptResolver
	HandoffResolver
	ModelSelector
	SessionController
	SubAgentInspector
	LoopController
	MemoryStatusReporter
	RoleController
	UsageReporter
	KeyHealthReporter
	CompactionController
	PlanExecutor
}

// SkillsStateProvider is implemented by agents that can expose currently
// available skill metadata to the TUI info panel.
type SkillsStateProvider interface {
	// ListSkills returns currently discoverable skills visible to the runtime.
	ListSkills() []*skill.Meta
}

// FocusedSkillsStateProvider is implemented by TUI backends that can expose
// skills for the currently focused agent without changing runtime provider semantics.
type FocusedSkillsStateProvider interface {
	FocusedSkills() []*skill.Meta
}

// FocusedSkillInvocationStateProvider is implemented by TUI backends that can
// expose per-skill visibility and load state for the currently focused agent,
// including manual-only skills the model never sees.
type FocusedSkillInvocationStateProvider interface {
	FocusedSkillInvocationStates() []skill.InvocationState
}

// LSPServerDisplay is one row in the ENVIRONMENT / LSP sidebar block.
type LSPServerDisplay struct {
	Name     string
	OK       bool
	Pending  bool // not connected yet (lazy start)
	Idle     bool // auto-unloaded while runtime is idle; will be restored on demand
	Err      string
	Errors   int
	Warnings int
}

// LSPStateProvider is an optional interface for agents that can expose per-file
// last-review LSP diagnostics (Write/Edit target file only, excluding related files)
// to the TUI info panel.
type LSPStateProvider interface {
	// LSPServerList returns configured language servers; nil/empty hides the LSP block.
	LSPServerList() []LSPServerDisplay
}

// MCPServerDisplay is one row in the TUI MCP sidebar.
type MCPServerDisplay struct {
	Name        string
	OK          bool
	Pending     bool // not connected yet (async startup)
	Idle        bool // auto-unloaded while runtime is idle; will be restored on demand
	Disabled    bool // explicitly disabled (manual /mcp disable)
	Manual      bool // configured as manual/on-demand; only manual servers can be changed with /mcp
	Enabled     bool // desired-enabled intent (manual servers): should be on, even if not connected yet
	Retrying    bool // transient failure, retry still in progress
	Attempt     int
	MaxAttempts int
	Err         string
}

// MCPStateProvider is implemented by agents that can expose MCP server status.
type MCPStateProvider interface {
	// MCPServerList returns every configured MCP with connection outcome.
	MCPServerList() []MCPServerDisplay
}
