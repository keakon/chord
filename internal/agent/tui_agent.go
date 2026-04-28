// Package agent defines the interface that the TUI uses to interact with
// either a local MainAgent or a remote agent (client connection).

package agent

import (
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

// AgentForTUI is the interface required by the TUI. It is implemented by the
// local [MainAgent] and by remote client adapters used in C/S mode.
type AgentForTUI interface {
	Events() <-chan AgentEvent

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

	// ResolveConfirm sends the user's confirmation response back to the pending
	// confirm flow.
	ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string)
	// ResolveQuestion sends the user's question response back to the pending
	// question flow.
	ResolveQuestion(answers []string, cancelled bool, requestID string)

	SwitchModel(providerModel string) error
	AvailableModels() []ModelOption
	ProviderModelRef() string
	// RunningModelRef is the effective provider/model for sidebar display: focused
	// SubAgent when any, otherwise MainAgent (may differ from ProviderModelRef during fallback).
	RunningModelRef() string
	// RunningVariant returns the active variant name for the running model, or empty string if none.
	RunningVariant() string

	GetSubAgents() []SubAgentInfo
	GetMessages() []message.Message
	SwitchFocus(agentID string)
	FocusedAgentID() string
	StartupResumeStatus() (pending bool, sessionID string)

	// ContinueFromContext re-runs the LLM using the existing context without
	// appending a new user message. Routes to focused SubAgent if one is active.
	ContinueFromContext()
	// RemoveLastMessage removes the last message from context and rewrites
	// persistence. Used before ContinueFromContext when last message is a
	// thinking-only assistant block that was interrupted.
	RemoveLastMessage()

	GetTokenUsage() message.TokenUsage
	// ProjectRoot returns the runtime project root directory.
	ProjectRoot() string
	// GetUsageStats returns session-wide totals (e.g. $ /stats Session overview and per-agent table).
	GetUsageStats() analytics.SessionStats
	// GetSidebarUsageStats returns token/cost totals for the focused agent only, aligned with
	// GetContextStats and GetTokenUsage for the right info panel and footer pills.
	GetSidebarUsageStats() analytics.SessionStats
	// GetContextStats returns current context usage and limit for the focused agent.
	// current is the last input token count (approximate context window usage); limit is the model context limit (0 if unknown).
	GetContextStats() (current, limit int)
	// GetContextMessageCount returns the number of messages in the focused agent's context (for sidebar). -1 if unknown.
	GetContextMessageCount() int
	// KeyStats returns (available, total) API keys for the focused agent's provider.
	KeyStats() (available, total int)
	// CurrentRateLimitSnapshot returns the latest rate-limit snapshot for the active key, or nil.
	CurrentRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot
	ProxyInUseForRef(ref string) bool
	CurrentRole() string
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

	ListSessionSummaries() ([]SessionSummary, error)
	GetSessionSummary() *SessionSummary
	DeleteSession(sessionID string) error
	ExportSession(format, path string)
	ResumeSession()
	ResumeSessionID(sessionID string)
	NewSession()
	// ForkSession creates a new session branching from the message at msgIndex.
	// The message at msgIndex becomes the draft loaded into the composer.
	// This is a local-TUI session-control capability; remote mode may leave it
	// unsupported until a dedicated protocol/API is defined.
	ForkSession(msgIndex int)

	// ExecutePlan triggers execution of a plan with the specified target agent.
	// agentName may be empty (defaults to "builder").
	ExecutePlan(planPath, agentName string)

	// AvailableAgents returns the names of agent roles available for Handoff.
	AvailableAgents() []string

	// SwitchRole requests the agent to switch its active role.
	// In embedded mode this calls switchRole directly; in C/S mode it sends
	// TypeSwitchRole to the server. The new role is broadcast as RoleChangedEvent.
	SwitchRole(role string)

	// AvailableRoles returns the ordered list of role names the user can cycle
	// through with the Tab key in the main agent view.
	AvailableRoles() []string

	// InvokedSkills returns skills explicitly loaded via the Skill tool in the current session.
	InvokedSkills() []*skill.Meta

	// GetTodos returns the current todo list for sidebar display.
	GetTodos() []tools.TodoItem

	// IsCompactionRunning reports whether a compaction goroutine is in flight.
	IsCompactionRunning() bool
	// CancelCompaction cancels an in-flight compaction. Returns true if there
	// was a running compaction to cancel.
	CancelCompaction() bool
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
