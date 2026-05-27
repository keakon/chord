// Package agent defines the interface that the TUI uses to interact with
// either a local MainAgent or a remote agent (client connection).

package agent

import (
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

// MessageSender covers user-message submission, queued drafts, and turn
// continuation. Implemented by [MainAgent] and remote-client adapters.
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

// PromptResolver delivers user responses for confirm/question dialogs back to
// the agent's pending interaction flow.
type PromptResolver interface {
	// ResolveConfirm sends the user's confirmation response back to the pending
	// confirm flow.
	ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string)
	// ResolveQuestion sends the user's question response back to the pending
	// question flow.
	ResolveQuestion(answers []string, cancelled bool, requestID string)
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

// SessionController exposes session lifecycle controls (resume, fork, delete,
// export). In remote mode some methods may be unavailable until a dedicated
// protocol/API is defined.
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

// ServiceTierReporter exposes effective runtime service-tier state for command completion and status UI.
type ServiceTierReporter interface {
	ServiceTier() config.ServiceTier
	EffectiveServiceTier() config.ServiceTier
}

// RoleController exposes role/handoff lifecycle for the active agent.
type RoleController interface {
	// SwitchRole requests the agent to switch its active role.
	// In embedded mode this calls switchRole directly; in C/S mode it sends
	// TypeSwitchRole to the server. The new role is broadcast as RoleChangedEvent.
	SwitchRole(role string)
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
	// GetContextStats returns current input-context usage and usable input budget for the focused agent.
	// current is the last input token count; limit is the usable input budget (0 if unknown).
	GetContextStats() (current, limit int)
	// GetContextMessageCount returns the number of messages in the focused agent's context (for sidebar). -1 if unknown.
	GetContextMessageCount() int
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
	// CancelCompaction cancels an in-flight compaction. Returns true if there
	// was a running compaction to cancel.
	CancelCompaction() bool
}

// PlanExecutor triggers plan-execution workflows.
type PlanExecutor interface {
	// ExecutePlan triggers execution of a plan with the specified target agent.
	// agentName may be empty (defaults to "builder").
	ExecutePlan(planPath, agentName string)
}

// AgentForTUI is the full interface required by the TUI. It is implemented by
// the local [MainAgent] and by remote client adapters used in C/S mode. New
// code that consumes only a slice of this surface should target the smaller
// sub-interfaces (MessageSender, ModelSelector, …) instead.
type AgentForTUI interface {
	Events() <-chan AgentEvent
	GetMessages() []message.Message
	StartupResumeStatus() (pending bool, sessionID string)
	// ProjectRoot returns the runtime project root directory.
	ProjectRoot() string
	// InvokedSkills returns skills explicitly loaded via the Skill tool in the current session.
	InvokedSkills() []*skill.Meta
	// GetTodos returns the current todo list for sidebar display.
	GetTodos() []tools.TodoItem

	MessageSender
	PromptResolver
	ModelSelector
	SessionController
	SubAgentInspector
	LoopController
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

// LSPServerDisplay is one row in the ENVIRONMENT / LSP sidebar block.
type LSPServerDisplay struct {
	Name     string
	OK       bool
	Pending  bool // not connected yet (lazy start)
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
	Disabled    bool // explicitly disabled (manual /mcp disable)
	Manual      bool // configured as manual/on-demand; only manual servers can be changed with /mcp
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
