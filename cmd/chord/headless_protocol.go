package main

import (
	"sync"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

const (
	// headlessMaxEnvelopeBytes is the encoded JSONL frame budget shared with
	// chord-gateway. It is large enough for bounded shell/tool results while
	// bounding the size written to the transport. Encoding still materializes
	// the envelope before enforcing this limit.
	headlessMaxEnvelopeBytes  = 16 * 1024 * 1024
	headlessStdinMaxLineBytes = 1 * 1024 * 1024
	// headlessGenesisSeq is the version of state that has never been pushed.
	// Status snapshots copy it; the first push bumps past it so a snapshot
	// taken before that push is strictly older.
	headlessGenesisSeq uint64 = 1
)

type headlessStdinLine struct {
	line  []byte
	code  string
	err   error
	fatal bool
}

type headlessHandoffPayload struct {
	RequestID string                     `json:"request_id"`
	PlanPath  string                     `json:"plan_path"`
	PlanText  string                     `json:"plan_text,omitempty"`
	PlanError string                     `json:"plan_error,omitempty"`
	Agents    []agent.HandoffAgentOption `json:"agents"`
}

type headlessRuleItem struct {
	Tool    string `json:"tool,omitempty"`
	Pattern string `json:"pattern"`
}

type headlessConfirmPayload struct {
	ToolName                string             `json:"tool_name"`
	ArgsJSON                string             `json:"args_json"`
	RequestID               string             `json:"request_id,omitempty"`
	TimeoutMS               int64              `json:"timeout_ms,omitempty"`
	NeedsApproval           []string           `json:"needs_approval,omitempty"`
	AlreadyAllowed          []string           `json:"already_allowed,omitempty"`
	NeedsApprovalRules      []string           `json:"needs_approval_rules,omitempty"`
	AlreadyAllowedRules     []string           `json:"already_allowed_rules,omitempty"`
	NeedsApprovalRuleItems  []headlessRuleItem `json:"needs_approval_rule_items,omitempty"`
	AlreadyAllowedRuleItems []headlessRuleItem `json:"already_allowed_rule_items,omitempty"`
	DoneReport              string             `json:"done_report,omitempty"`
	DoneReason              string             `json:"done_reason,omitempty"`
	AgentID                 string             `json:"agent_id,omitempty"`
}

type headlessQuestionPayload struct {
	BindingID       string                `json:"binding_id"`
	TaskID          string                `json:"task_id"`
	ScopeID         string                `json:"request_scope_id"`
	Version         uint64                `json:"version"`
	ResponsePolicy  string                `json:"response_policy"`
	DefaultOptionID string                `json:"default_option_id,omitempty"`
	OptionIDs       []string              `json:"option_ids,omitempty"`
	Timer           string                `json:"timer"`
	DurationSeconds int64                 `json:"duration_seconds,omitempty"`
	Outcome         string                `json:"reason,omitempty"`
	ResultID        string                `json:"result_id,omitempty"`
	Selected        []string              `json:"selected,omitempty"`
	SelectedIDs     []string              `json:"selected_ids,omitempty"`
	ToolName        string                `json:"tool_name"`
	Header          string                `json:"header,omitempty"`
	Question        string                `json:"question"`
	Options         []string              `json:"options"`
	OptionDetails   []string              `json:"option_details,omitempty"`
	Multiple        bool                  `json:"multiple,omitempty"`
	RequestID       string                `json:"request_id,omitempty"`
	Deadline        *time.Time            `json:"deadline,omitempty"`
	AgentID         string                `json:"agent_id,omitempty"`
	RevisionResult  *tools.QuestionAnswer `json:"revision_result,omitempty"`
}

// headlessHandoffCancelledReasonSuperseded is the reason carried by a
// handoff_cancelled event when a pending handoff is discarded without a client
// decision, mirroring the runtime's own "superseded" reason. Clients use it to
// tell an overridden handoff apart from a client-driven cancel.
const headlessHandoffCancelledReasonSuperseded = "superseded"

// headlessState holds mutex-protected state for the headless protocol.
type headlessState struct {
	mcpRole        string
	mcpSession     string
	mcp            mcp.RuntimeSnapshot
	inputQueue     *headlessInputQueue
	mu             sync.Mutex
	busy           bool
	phase          string
	phaseDetail    string
	pendingConfirm *headlessConfirmPayload
	questions      map[string]*headlessQuestionPayload
	pendingHandoff *headlessHandoffPayload
	lastError      string
	pendingOutcome string // "completed" / "cancelled" / "error" / ""
	lastOutcome    string // persists across idle; set from pendingOutcome on idle
	role           string // current main role; filled from RoleChangedEvent / status queries
	// lastBroadcastRole is the role of the most recent role_change announcement
	// or committed role set, or "" before the first switch. state.role alone is
	// not enough to dedupe RoleChangedEvent: role set responses update the
	// cache, and comparing the trailing event against that cache would swallow
	// the only announcement of a successful switch (the response is a command
	// reply, not a pushed event). Announcements are therefore compared against
	// this separate already-announced marker.
	lastBroadcastRole string
	updatedAt         time.Time
	// sessionID is the active session directory base name reported in
	// status_response. It is seeded at startup and refreshed when a
	// SessionRestoredEvent resolves a different backend session: an in-band
	// switch (handoff plan execution, /resume <id>) replaces the session
	// without restarting the process. A change is announced with an explicit
	// session_switched push; the cached value alone never counts as the
	// gateway having seen the new session.
	sessionID    string
	workspace    string
	runtimeID    string
	sessionEpoch uint64
	workDir      agent.WorkDirSnapshot
	// seq is a monotonic version counter for emitted state. Genesis is 1, so a
	// status_response copied before any push still has a nonzero seq on the
	// wire (uint64 0 would be omitted by json omitempty and look unversioned).
	// Pushes bump under mu — both the event-loop batches from
	// filterHeadlessEvent and command-path announcements such as role_change
	// and handoff_cancelled — and each push's stamp and channel enqueue share
	// one stdoutWriter.ordered section, so pushes reach the wire in seq order.
	// Any cached-state mutation bumps the same way,
	// even when the gateway did not subscribe to the corresponding push (or
	// the mutation has no push at all, like auto-dismissing a pending
	// confirm), so a later snapshot is strictly newer than one copied before
	// the mutation. status_response snapshots read the current value without
	// bumping: a snapshot whose seq is smaller than an already-seen version
	// was copied before that mutation and must not be applied.
	seq uint64

	// subscriptions is the set of event types the gateway wants to receive.
	// If nil, no subscribe command has been received and all event types are
	// forwarded by default. An explicit empty map means the gateway subscribed
	// only to unknown/removed event types, so no optional events are forwarded.
	subscriptions map[string]bool
}

// isSubscribed returns true if the given event type should be forwarded.
// Caller must hold s.mu: subscribe replaces the map while the event loop
// and command path read it.
func (s *headlessState) isSubscribed(eventType string) bool {
	if s.subscriptions == nil {
		return true // default before subscribe: all events
	}
	return s.subscriptions[eventType]
}

// stampHeadlessSeq versions state-carrying envelopes. Caller must hold s.mu.
// Snapshots (bump=false) reuse the current version so a later push can
// overtake them; pushes (bump=true) allocate a newer version first.
func (s *headlessState) stampHeadlessSeq(bump bool, envs ...*headlessEnvelope) uint64 {
	if s.seq < headlessGenesisSeq {
		s.seq = headlessGenesisSeq
	}
	if bump {
		s.seq++
	}
	for _, env := range envs {
		if env != nil {
			env.Seq = s.seq
		}
	}
	return s.seq
}

// headlessEnvelope is the JSON envelope for stdio protocol messages.
// Seq carries the state version for state-carrying envelopes (event-loop
// pushes, command-path announcements, and status_response snapshots); it is
// omitted everywhere else.
type headlessEnvelope struct {
	Type    string `json:"type"`
	Seq     uint64 `json:"seq,omitempty"`
	Payload any    `json:"payload,omitempty"`
}

// headlessCommand represents a command received from stdin.
type headlessCommand struct {
	InputTarget           *agent.InputTarget           `json:"target,omitempty"`
	InputID               string                       `json:"input_id,omitempty"`
	ExpectedRevision      *uint64                      `json:"expected_revision,omitempty"`
	ExpectedQueueRevision *uint64                      `json:"expected_queue_revision,omitempty"`
	ExpectedViewVersion   *uint64                      `json:"expected_view_version,omitempty"`
	DeliveryPolicy        recovery.InputDeliveryPolicy `json:"delivery_policy,omitempty"`
	ContentPresent        bool                         `json:"-"`

	BindingID           string             `json:"binding_id"`
	QuestionsAfterID    string             `json:"questions_after_id,omitempty"`
	QuestionIDs         []string           `json:"question_ids,omitempty"`
	Type                string             `json:"type"`
	Content             string             `json:"content,omitempty"`
	Command             string             `json:"command,omitempty"`
	RequestID           string             `json:"request_id,omitempty"`
	Action              string             `json:"action,omitempty"`
	Agent               string             `json:"agent,omitempty"`
	Pool                string             `json:"pool,omitempty"`
	Role                string             `json:"role,omitempty"`
	FinalArgsJSON       string             `json:"final_args_json,omitempty"`
	EditSummary         string             `json:"edit_summary,omitempty"`
	DenyReason          string             `json:"deny_reason,omitempty"`
	RulePattern         string             `json:"rule_pattern,omitempty"`
	RuleTool            string             `json:"rule_tool,omitempty"`  // defaults to the confirmed tool
	RuleItems           []headlessRuleItem `json:"rule_items,omitempty"` // structured alternative for multiple rules
	RuleScope           string             `json:"rule_scope,omitempty"` // session | project | user_global
	Answers             []string           `json:"answers,omitempty"`
	OperationID         string             `json:"operation_id,omitempty"`
	Version             uint64             `json:"version,omitempty"`
	Custom              bool               `json:"custom,omitempty"`
	ReplacementID       string             `json:"replacement_id,omitempty"`
	UserText            string             `json:"user_text,omitempty"`
	SupportsInteraction bool               `json:"supports_interaction,omitempty"`
	Events              []string           `json:"events,omitempty"` // for subscribe command
}

// All available push event types that can be subscribed to.
var headlessEventTypes = map[string]bool{
	headlessImageResult:          true,
	"activity":                   true,
	"assistant_message":          true,
	"idle":                       true,
	"confirm_request":            true,
	"confirm_resolved":           true,
	"confirm_rule_intent_result": true,
	"question_request":           true,
	"question_resolved":          true,
	"question_updated":           true,
	"role_change":                true,
	"notification":               true,
	"workdir_changed":            true,
	"handoff_request":            true,
	"handoff_cancelled":          true,
	"error":                      true,
	"agent_started":              true,
	"agent_notify":               true,
	"agent_done":                 true,
	"info":                       true,
	"toast":                      true,
	"done_completion":            true,
	"local_shell_result":         true,
	"assistant_rollback":         true,
	"todos":                      true,
	"compaction_status":          true,
	"session_switched":           true,
	"background_result":          true,
	"context_notice":             true,
	headlessMCPStatusChanged:     true,
	"input_queue_changed":        true,
}
