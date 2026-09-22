// Package main provides the chord headless subcommand for stdio JSON protocol mode.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

const (
	headlessStdinMaxLineBytes = 1024 * 1024
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

type headlessConfirmPayload struct {
	ToolName            string   `json:"tool_name"`
	ArgsJSON            string   `json:"args_json"`
	RequestID           string   `json:"request_id,omitempty"`
	TimeoutMS           int64    `json:"timeout_ms,omitempty"`
	NeedsApproval       []string `json:"needs_approval,omitempty"`
	AlreadyAllowed      []string `json:"already_allowed,omitempty"`
	NeedsApprovalRules  []string `json:"needs_approval_rules,omitempty"`
	AlreadyAllowedRules []string `json:"already_allowed_rules,omitempty"`
	DoneReport          string   `json:"done_report,omitempty"`
	DoneReason          string   `json:"done_reason,omitempty"`
	AgentID             string   `json:"agent_id,omitempty"`
}

type headlessQuestionPayload struct {
	ToolName      string     `json:"tool_name"`
	Header        string     `json:"header,omitempty"`
	Question      string     `json:"question"`
	Options       []string   `json:"options"`
	OptionDetails []string   `json:"option_details,omitempty"`
	Multiple      bool       `json:"multiple,omitempty"`
	RequestID     string     `json:"request_id,omitempty"`
	Deadline      *time.Time `json:"deadline,omitempty"`
	AgentID       string     `json:"agent_id,omitempty"`
}

// headlessHandoffCancelledReasonSuperseded is the reason carried by a
// handoff_cancelled event when a pending handoff is discarded without a client
// decision, mirroring the runtime's own "superseded" reason. Clients use it to
// tell an overridden handoff apart from a client-driven cancel.
const headlessHandoffCancelledReasonSuperseded = "superseded"

// headlessState holds mutex-protected state for the headless protocol.
type headlessState struct {
	mu              sync.Mutex
	busy            bool
	phase           string
	phaseDetail     string
	pendingConfirm  *headlessConfirmPayload
	pendingQuestion *headlessQuestionPayload
	pendingHandoff  *headlessHandoffPayload
	lastError       string
	pendingOutcome  string // "completed" / "cancelled" / "error" / ""
	lastOutcome     string // persists across idle; set from pendingOutcome on idle
	role            string // current main role; filled from RoleChangedEvent / status queries
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
	sessionID string
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

// stdoutWriter serializes JSON envelopes to stdout via a single goroutine.
type stdoutWriter struct {
	enc       *json.Encoder
	ch        chan any
	ctx       context.Context
	orderMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
}

// newStdoutWriter creates a stdoutWriter with a buffered channel.
func newStdoutWriter(ctx context.Context, w io.Writer) *stdoutWriter {
	return &stdoutWriter{
		enc:  json.NewEncoder(w),
		ch:   make(chan any, 256),
		ctx:  ctx,
		done: make(chan struct{}),
	}
}

// run processes the channel and writes JSON envelopes to stdout.
func (w *stdoutWriter) run() {
	defer close(w.done)
	for msg := range w.ch {
		_ = w.enc.Encode(msg)
	}
}

// ordered runs f while holding the push-ordering lock. Seq allocation and the
// channel enqueue for a state-carrying push must happen inside one ordered
// section (on both the event-loop and the command path): otherwise a concurrent
// push could allocate the next seq and reach the wire in the stamp-to-emit gap,
// and a client that applies only newer versions would drop the older push. For
// role_change that loss is permanent — the announcement dedupe marker already
// advanced at stamp time, so the switch is never re-announced.
//
// Sections must not take this lock while holding state.mu, and f must not block
// indefinitely: emit under the lock only ever waits on the channel buffer
// (stdout backpressure), which the writer goroutine drains independently.
func (w *stdoutWriter) ordered(f func()) {
	w.orderMu.Lock()
	defer w.orderMu.Unlock()
	f()
}

// emit sends a message to the channel. It blocks if the channel is full
// (stdio is a reliable pipe; control requests, SubAgent lifecycle events,
// and response events must never be silently dropped).
// Returns false if the context was cancelled before the message could be sent.
func (w *stdoutWriter) emit(msg any) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	select {
	case w.ch <- msg:
		return true
	case <-w.ctx.Done():
		return false
	}
}

func (w *stdoutWriter) close() {
	if w == nil {
		return
	}
	w.closeOnce.Do(func() {
		close(w.ch)
		<-w.done
	})
}

// headlessCommand represents a command received from stdin.
type headlessCommand struct {
	Type          string   `json:"type"`
	Content       string   `json:"content,omitempty"`
	Command       string   `json:"command,omitempty"`
	RequestID     string   `json:"request_id,omitempty"`
	Action        string   `json:"action,omitempty"`
	Agent         string   `json:"agent,omitempty"`
	Pool          string   `json:"pool,omitempty"`
	Role          string   `json:"role,omitempty"`
	FinalArgsJSON string   `json:"final_args_json,omitempty"`
	EditSummary   string   `json:"edit_summary,omitempty"`
	DenyReason    string   `json:"deny_reason,omitempty"`
	RulePattern   string   `json:"rule_pattern,omitempty"`
	RuleScope     string   `json:"rule_scope,omitempty"` // session | project | user_global
	Answers       []string `json:"answers,omitempty"`
	Reason        string   `json:"reason,omitempty"` // for question: answered | declined
	Events        []string `json:"events,omitempty"` // for subscribe command
}

// All available push event types that can be subscribed to.
var headlessEventTypes = map[string]bool{
	"activity":           true,
	"assistant_message":  true,
	"idle":               true,
	"confirm_request":    true,
	"question_request":   true,
	"question_resolved":  true,
	"role_change":        true,
	"notification":       true,
	"handoff_request":    true,
	"handoff_cancelled":  true,
	"error":              true,
	"agent_started":      true,
	"agent_notify":       true,
	"agent_done":         true,
	"info":               true,
	"toast":              true,
	"done_completion":    true,
	"local_shell_result": true,
	"assistant_rollback": true,
	"todos":              true,
	"compaction_status":  true,
	"session_switched":   true,
	"background_result":  true,
	"context_notice":     true,
}

// filterHeadlessEvent converts an AgentEvent to one or more headlessEnvelopes.
// Returns nil if the event should be filtered out (not subscribed).
func filterHeadlessEvent(ev agent.AgentEvent, state *headlessState, backends ...headlessBackend) []*headlessEnvelope {
	var backend headlessBackend
	if len(backends) > 0 {
		backend = backends[0]
	}
	// Handoff plan file IO and agent-option lookup stay outside state.mu: both
	// can block while the event loop and command handlers contend on the lock.
	var preHandoffAgents []agent.HandoffAgentOption
	var preHandoffPlanText, preHandoffPlanError string
	if he, ok := ev.(agent.HandoffEvent); ok {
		preHandoffAgents = []agent.HandoffAgentOption{}
		if hb, ok := backend.(headlessHandoffBackend); ok {
			preHandoffAgents = hb.HandoffAgentOptions()
		}
		if preHandoffAgents == nil {
			preHandoffAgents = []agent.HandoffAgentOption{}
		}
		if b, err := os.ReadFile(he.PlanPath); err == nil {
			preHandoffPlanText = string(b)
		} else {
			preHandoffPlanError = err.Error()
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()

	var out []*headlessEnvelope
	// mutated tracks whether cached state changed: any mutation must bump seq
	// even when no envelope is emitted (unsubscribed), so later
	// status_response snapshots stay strictly newer than earlier ones.
	mutated := false
	// touch records a cached-state mutation: it marks the state dirty for the
	// seq bump above and timestamps the change.
	touch := func() {
		mutated = true
		state.updatedAt = time.Now()
	}

	switch e := ev.(type) {
	case agent.AgentActivityEvent:
		if e.Type == agent.ActivityIdle {
			return nil // filtered; idle is expressed via GlobalIdleEvent
		}
		touch()
		state.busy = true
		state.phase = string(e.Type)
		state.phaseDetail = e.Detail
		if e.Type == agent.ActivityCompacting {
			// Don't modify pendingOutcome for compacting
		} else if state.pendingOutcome != "error" && state.pendingOutcome != "cancelled" {
			state.pendingOutcome = "completed"
		}
		if state.isSubscribed("activity") {
			out = append(out, &headlessEnvelope{Type: "activity", Payload: map[string]string{
				"agent_id": e.AgentID,
				"type":     string(e.Type),
				"detail":   e.Detail,
			}})
		}
	case agent.CompactionStatusEvent:
		touch()
		// The control plane observes started and terminal outcomes only;
		// internal progress telemetry stays on the TUI slot.
		if e.Status == agent.CompactionStatusProgress {
			state.stampHeadlessSeq(true)
			return nil
		}
		if state.isSubscribed("compaction_status") {
			out = append(out, &headlessEnvelope{Type: "compaction_status", Payload: map[string]any{
				"status":    e.Status,
				"trigger":   e.Trigger,
				"reason":    e.Reason,
				"plan_id":   e.PlanID,
				"synthetic": e.Synthetic,
			}})
		}
	case agent.AssistantMessageEvent:
		touch()
		if strings.TrimSpace(e.Text) == "" {
			log.Warnf("headless observed empty assistant_message agent_id=%v tool_calls=%v", e.AgentID, e.ToolCalls)
		} else {
			log.Debugf("headless forwarding assistant_message agent_id=%v text_len=%v tool_calls=%v", e.AgentID, len(e.Text), e.ToolCalls)
		}
		if state.isSubscribed("assistant_message") {
			out = append(out, &headlessEnvelope{Type: "assistant_message", Payload: map[string]any{
				"agent_id":        e.AgentID,
				"task_id":         e.TaskID,
				"agent_type":      e.AgentType,
				"parent_agent_id": e.ParentAgentID,
				"text":            e.Text,
				"tool_calls":      e.ToolCalls,
			}})
		}
	case agent.AgentStartedEvent:
		touch()
		if state.isSubscribed("agent_started") {
			out = append(out, &headlessEnvelope{Type: "agent_started", Payload: map[string]string{
				"agent_id":          e.AgentID,
				"previous_agent_id": e.PreviousAgentID,
				"task_id":           e.TaskID,
				"agent_type":        e.AgentType,
				"description":       e.Description,
				"parent_agent_id":   e.ParentAgentID,
				"parent_task_id":    e.ParentTaskID,
			}})
		}
	case agent.AgentNotifyEvent:
		touch()
		if state.isSubscribed("agent_notify") {
			payload := map[string]string{
				"agent_id":        e.AgentID,
				"task_id":         e.TaskID,
				"agent_type":      e.AgentType,
				"parent_agent_id": e.ParentAgentID,
				"parent_task_id":  e.ParentTaskID,
				"target_agent_id": e.TargetAgentID,
				"target_task_id":  e.TargetTaskID,
				"kind":            e.Kind,
				"message":         e.Message,
			}
			// Subtype distinguishes an alert that already resolved (e.g. stall →
			// stall_resolved) from one still pending. The key is omitted when
			// absent so the envelope shape stays backward compatible.
			if e.Subtype != "" {
				payload["subtype"] = e.Subtype
			}
			out = append(out, &headlessEnvelope{Type: "agent_notify", Payload: payload})
		}
	case agent.IdleEvent:
		touch()
	case agent.GlobalIdleEvent:
		touch()
		state.busy = false
		state.phase = ""
		state.phaseDetail = ""
		outcome := state.pendingOutcome
		state.lastOutcome = outcome
		state.pendingOutcome = ""
		state.pendingConfirm = nil
		state.pendingQuestion = nil
		state.lastError = ""
		if state.isSubscribed("idle") {
			out = append(out, &headlessEnvelope{Type: "idle", Payload: map[string]any{
				"last_outcome":               outcome,
				"suppress_user_notification": e.SuppressUserNotification,
			}})
		}
	case agent.RoleChangedEvent:
		// A RoleChangedEvent is emitted right after the agent commits a switch,
		// but it travels through a separate event stream and can drain after a
		// newer role set has already updated the cache, so trusting the event
		// verbatim could regress state.role to an older role. Resolve the role
		// from the backend's committed state (the event never leads the backend)
		// and push role_change when this successful switch has not been
		// announced yet. The dedup key is lastBroadcastRole — the last role
		// actually announced — not state.role, whose cache can be refreshed by a
		// role set response before any role_change envelope was emitted. Falls
		// back to the event's role when the backend is not role-capable.
		role := e.Role
		if rb, ok := backend.(headlessRoleBackend); ok {
			if current := rb.CurrentRole(); current != "" {
				role = current
			}
		}
		changed := state.lastBroadcastRole != role
		touch()
		state.role = role
		if changed {
			state.lastBroadcastRole = role
		}
		if changed && state.isSubscribed("role_change") {
			out = append(out, &headlessEnvelope{Type: "role_change", Payload: map[string]string{
				"role": role,
			}})
		}
	case agent.NotificationEvent:
		touch()
		if state.isSubscribed("notification") {
			out = append(out, &headlessEnvelope{Type: "notification", Payload: map[string]string{
				"reason":  e.Reason,
				"message": e.Message,
			}})
		}
	case agent.ErrorEvent:
		if e.Silent {
			// Silent retry telemetry never reaches the user: the TUI records
			// it in the error panel without settling cards or rendering an
			// error block. Promoting it here would flip last_outcome to
			// "error" for turns that recover and complete. Dropping it is
			// safe because every silent-only sequence is eventually followed
			// by a non-silent error on terminal failure; any new path that
			// emits only silent errors must also emit a non-silent terminal
			// error.
			touch()
			state.stampHeadlessSeq(true)
			return nil
		}
		touch()
		state.pendingOutcome = "error"
		if e.Err != nil {
			state.lastError = e.Err.Error()
		}
		if state.isSubscribed("error") {
			out = append(out, &headlessEnvelope{Type: "error", Payload: map[string]string{
				"message":  state.lastError,
				"agent_id": e.AgentID,
			}})
		}
	case agent.ConfirmRequestEvent:
		touch()
		doneReason, doneReport := parseHeadlessDoneArgs(e.ArgsJSON)
		if strings.TrimSpace(e.DoneReport) != "" {
			doneReport = strings.TrimSpace(e.DoneReport)
		}
		state.pendingConfirm = &headlessConfirmPayload{ToolName: e.ToolName, ArgsJSON: e.ArgsJSON, RequestID: e.RequestID, TimeoutMS: e.Timeout.Milliseconds(), NeedsApproval: e.NeedsApproval, AlreadyAllowed: e.AlreadyAllowed, NeedsApprovalRules: e.NeedsApprovalRules, AlreadyAllowedRules: e.AlreadyAllowedRules, DoneReport: doneReport, DoneReason: doneReason, AgentID: e.AgentID}
		if state.isSubscribed("confirm_request") {
			out = append(out, &headlessEnvelope{Type: "confirm_request", Payload: map[string]any{
				"tool_name":             e.ToolName,
				"args_json":             e.ArgsJSON,
				"request_id":            e.RequestID,
				"timeout_ms":            e.Timeout.Milliseconds(),
				"needs_approval":        e.NeedsApproval,
				"already_allowed":       e.AlreadyAllowed,
				"needs_approval_rules":  e.NeedsApprovalRules,
				"already_allowed_rules": e.AlreadyAllowedRules,
				"done_report":           doneReport,
				"done_reason":           doneReason,
				"agent_id":              e.AgentID,
			}})
		}
	case agent.QuestionRequestEvent:
		touch()
		var deadline *time.Time
		if !e.Deadline.IsZero() {
			d := e.Deadline
			deadline = &d
		}
		state.pendingQuestion = &headlessQuestionPayload{ToolName: e.ToolName, Header: e.Header, Question: e.Question, Options: e.Options, OptionDetails: e.OptionDetails, Multiple: e.Multiple, RequestID: e.RequestID, Deadline: deadline, AgentID: e.AgentID}
		if state.isSubscribed("question_request") {
			payload := map[string]any{
				"tool_name":      e.ToolName,
				"header":         e.Header,
				"question":       e.Question,
				"options":        e.Options,
				"option_details": e.OptionDetails,
				"multiple":       e.Multiple,
				"request_id":     e.RequestID,
				"agent_id":       e.AgentID,
			}
			// A question with no configured timeout has no close time. Omitting
			// the key keeps the wire shape the docs promise; writing a nil
			// pointer would encode as "deadline": null.
			if deadline != nil {
				payload["deadline"] = deadline
			}
			out = append(out, &headlessEnvelope{Type: "question_request", Payload: payload})
		}
	case agent.QuestionResolvedEvent:
		// The core close event is the authoritative pending source: clear the
		// cached request only when it matches, so a stale close never removes a
		// newer question.
		if state.pendingQuestion != nil && state.pendingQuestion.RequestID == e.RequestID {
			touch()
			state.pendingQuestion = nil
		}
		if state.isSubscribed("question_resolved") {
			out = append(out, &headlessEnvelope{Type: "question_resolved", Payload: map[string]string{
				"request_id": e.RequestID,
				"reason":     e.Reason,
			}})
		}
	case agent.HandoffEvent:
		// Offer exactly the runtime's eligible targets; an empty list means no
		// legal handoff target exists (the active role is the only main-mode
		// agent) and must reach the client as-is instead of a fabricated
		// default the runtime would reject on approval.
		// Agent options and plan file contents were loaded before acquiring
		// state.mu; only the cache update runs under the lock.
		touch()
		payload := &headlessHandoffPayload{
			RequestID: e.RequestID,
			PlanPath:  e.PlanPath,
			Agents:    preHandoffAgents,
			PlanText:  preHandoffPlanText,
			PlanError: preHandoffPlanError,
		}
		state.pendingHandoff = payload
		if state.isSubscribed("handoff_request") {
			out = append(out, &headlessEnvelope{Type: "handoff_request", Payload: payload})
		}
	case agent.HandoffCancelledEvent:
		touch()
		// A new turn or session switch discarded the pending handoff before the
		// client decided. Only clear the cached request when this cancellation
		// targets it (an empty RequestID cancels whatever is pending); a stale
		// cancellation for an older request must not drop a newer pending one.
		if state.pendingHandoff != nil && (e.RequestID == "" || state.pendingHandoff.RequestID == e.RequestID) {
			state.pendingHandoff = nil
		}
		// Forward even when it did not match the cached request: that request is
		// truly gone, and the client must stop waiting on it.
		if state.isSubscribed("handoff_cancelled") {
			out = append(out, &headlessEnvelope{Type: "handoff_cancelled", Payload: map[string]string{
				"request_id": e.RequestID,
				"reason":     e.Reason,
			}})
		}
	case agent.AgentDoneEvent:
		touch()
		if state.isSubscribed("agent_done") {
			out = append(out, &headlessEnvelope{Type: "agent_done", Payload: map[string]string{
				"agent_id":        e.AgentID,
				"task_id":         e.TaskID,
				"agent_type":      e.AgentType,
				"parent_agent_id": e.ParentAgentID,
				"parent_task_id":  e.ParentTaskID,
				"summary":         e.Summary,
			}})
		}
	case agent.InfoEvent:
		if state.isSubscribed("info") {
			out = append(out, &headlessEnvelope{Type: "info", Payload: map[string]string{"message": e.Message, "agent_id": e.AgentID}})
		}
	case agent.ToolResultEvent:
		touch()
		if strings.EqualFold(e.Name, tools.NameDone) && e.AgentID == "" {
			reason, report := parseHeadlessDoneArgs(e.ArgsJSON)
			if strings.TrimSpace(e.DoneReport) != "" {
				report = strings.TrimSpace(e.DoneReport)
			}
			if report != "" && state.isSubscribed("done_completion") {
				out = append(out, &headlessEnvelope{Type: "done_completion", Payload: map[string]any{"call_id": e.CallID, "report": report, "reason": reason, "status": string(e.Status), "agent_id": e.AgentID, "mode": "normal"}})
			}
		}
	case agent.StreamRollbackEvent:
		touch()
		if state.isSubscribed("assistant_rollback") {
			out = append(out, &headlessEnvelope{Type: "assistant_rollback", Payload: map[string]string{"reason": e.Reason, "agent_id": e.AgentID}})
		}
	case agent.TodosUpdatedEvent:
		touch()
		if state.isSubscribed("todos") {
			out = append(out, &headlessEnvelope{Type: "todos", Payload: map[string]any{"todos": e.Todos}})
		}
	case agent.ToastEvent:
		if state.isSubscribed("toast") {
			out = append(out, &headlessEnvelope{Type: "toast", Payload: map[string]string{"message": e.Message, "level": e.Level, "agent_id": e.AgentID}})
		}
	case agent.SessionRestoredEvent:
		// An in-band session switch (handoff plan execution, /resume <id>)
		// replaces the session without restarting the process, so the startup
		// session snapshot alone would leave status_response and the gateway
		// pin on the old session. Refresh the tracked id from the backend's
		// committed state and announce the change explicitly. Restores that
		// keep the session (startup replay, durable compaction rewrite) only
		// refresh the timestamp.
		touch()
		sessionID := headlessBackendSessionID(backend)
		if sessionID == "" || sessionID == state.sessionID {
			// No committed id to adopt, or the id is already the tracked one:
			// either way there is nothing new to announce.
			break
		}
		state.sessionID = sessionID
		if state.isSubscribed("session_switched") {
			out = append(out, &headlessEnvelope{Type: "session_switched", Payload: map[string]string{
				"session_id": sessionID,
			}})
		}
	case agent.BackgroundResultAppendedEvent:
		// A finished background job's result is durable now, and this event is
		// the only delivery channel for the JOB RESULT card: background output
		// typically lands after the turn is idle, so no later
		// assistant_message summarizes it.
		touch()
		if state.isSubscribed("background_result") {
			out = append(out, &headlessEnvelope{Type: "background_result", Payload: map[string]any{
				"session_id":      state.sessionID,
				"target_agent_id": e.TargetAgentID,
				"message_index":   e.MessageIndex,
				"content":         e.Message.Content,
			}})
		}
	case agent.ContextNoticeEvent:
		// Durable context-pressure warning with no other headless channel (no
		// toast/info accompanies it). Gateway notifications are
		// fire-and-forget, so there is no card state for
		// ContextNoticeClearedEvent to retract; the cleared event stays
		// TUI-only by design.
		touch()
		if state.isSubscribed("context_notice") {
			out = append(out, &headlessEnvelope{Type: "context_notice", Payload: map[string]any{
				"session_id":    state.sessionID,
				"level":         e.Level,
				"message":       e.Message,
				"message_index": e.MessageIndex,
			}})
		}
	}
	if len(out) == 0 {
		if mutated {
			state.stampHeadlessSeq(true)
		}
		return nil
	}
	state.stampHeadlessSeq(true, out...)
	return out
}

func parseHeadlessDoneArgs(argsJSON string) (reason, report string) {
	if strings.TrimSpace(argsJSON) == "" {
		return "", ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", ""
	}
	reason, _ = args["reason"].(string)
	report, _ = args["report"].(string)
	return strings.TrimSpace(reason), strings.TrimSpace(report)
}

// isUnsupportedHeadlessCommand checks if a command is only available in TUI mode.
func isUnsupportedHeadlessCommand(content string) bool {
	fields := strings.Fields(strings.TrimSpace(content))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/export":
		return true
	case "/resume":
		return len(fields) == 1
	case "/mcp":
		// Supported in headless mode when passed explicitly with an action.
		return len(fields) == 1
	default:
		return false
	}
}

// newHeadlessCmd creates and returns the Cobra command for headless stdio mode.
func newHeadlessCmd() *cobra.Command {
	var (
		flagHeadlessDir      string
		flagHeadlessContinue bool
		flagHeadlessResume   string
		flagHeadlessWorktree string
	)

	cmd := &cobra.Command{
		Use:   "headless",
		Short: "Run chord without TUI (stdio JSON protocol)",
		Long: `Run chord in headless mode with stdio JSON protocol.

In headless mode, chord communicates via JSON lines over stdin/stdout,
suitable for integration with external tools or gateways.

After startup, chord emits a "ready" event. The gateway can then send a
"subscribe" command to select which event types it wants to receive.
If no subscribe command is sent, all event types are forwarded.

Examples:
  chord headless -d /path/to/project
  chord headless --continue
  chord headless --resume <session-id>
  chord headless -d /path/to/project --worktree feat-auth

Model pool control commands:
  {"type":"models","action":"status"}
  {"type":"models","action":"set_current_model_pool","pool":"thinking"}

Main role control commands:
  {"type":"role","action":"list"}
  {"type":"role","action":"set","role":"planner"}`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flagHeadlessDir != "" {
				if err := os.Chdir(flagHeadlessDir); err != nil {
					return fmt.Errorf("change to session-dir %q: %w", flagHeadlessDir, err)
				}
			}
			if cmd.Flags().Changed("worktree") {
				wtCtx := cmd.Context()
				if wtCtx == nil {
					wtCtx = context.Background()
				}
				info, err := prepareStartupWorktree(wtCtx, flagHeadlessWorktree, flagHeadlessResetBranch)
				if err != nil {
					return err
				}
				flagWorktreeStartupInfo = info
				flagWorktreeStartupMeta = worktreeMetaForInfo(info)
				flagWorktreeStartupReason = recovery.WorktreeSwitchCreate
			}
			if flagHeadlessContinue {
				flagContinueSession = true
			}
			if flagHeadlessResume != "" {
				flagResumeSession = flagHeadlessResume
			}
			if flagContinueSession && flagResumeSession != "" {
				return fmt.Errorf("--continue and --resume are mutually exclusive")
			}
			// A resumed session remembers the checkout it was working in.
			// Enter it before initApp so tools, the LSP root and git status
			// are anchored there; an explicit --worktree stays authoritative.
			if flagWorktreeStartupInfo == nil && flagResumeSession != "" {
				wtCtx := cmd.Context()
				if wtCtx == nil {
					wtCtx = context.Background()
				}
				if info := resumeSessionWorktree(wtCtx, flagResumeSession); info != nil {
					if err := os.Chdir(info.Path); err != nil {
						return fmt.Errorf("chdir to worktree %q: %w", info.Name, err)
					}
					flagWorktreeStartupInfo = info
					flagWorktreeStartupMeta = worktreeMetaForInfo(info)
					flagWorktreeStartupReason = recovery.WorktreeSwitchResume
				}
			}
			return runHeadless(cmd, nil)
		},
	}

	cmd.Flags().StringVarP(&flagHeadlessDir, "session-dir", "d", "", "Project directory (session directory)")
	cmd.Flags().BoolVarP(&flagHeadlessContinue, "continue", "c", false, "Continue the latest session")
	cmd.Flags().StringVarP(&flagHeadlessResume, "resume", "r", "", "Resume a specific session ID")
	cmd.Flags().StringVarP(&flagHeadlessWorktree, "worktree", "w", "", "Create or enter a chord-managed git worktree by name (auto-named when empty); sessions are shared by every checkout of the repository")
	cmd.Flags().Lookup("worktree").NoOptDefVal = ""
	cmd.Flags().BoolVar(&flagHeadlessResetBranch, "reset-branch", false, "Reset an existing branch that no worktree has checked out to HEAD instead of refusing to recreate it (only with --worktree)")

	return cmd
}

// headlessRuntime is the small runtime surface required by headless stdio mode.
type headlessRuntime interface {
	Close()
	Events() <-chan agent.AgentEvent
	Backend() headlessBackend
}

type runtimeHeadlessAdapter struct {
	rt *Runtime
}

func (a runtimeHeadlessAdapter) Close() {
	if a.rt != nil {
		a.rt.Close()
	}
}

func (a runtimeHeadlessAdapter) Events() <-chan agent.AgentEvent {
	if a.rt == nil || a.rt.Agent == nil {
		ch := make(chan agent.AgentEvent)
		close(ch)
		return ch
	}
	return a.rt.Agent.Events()
}

func (a runtimeHeadlessAdapter) Backend() headlessBackend {
	if a.rt == nil {
		return nil
	}
	return a.rt.Agent
}

type headlessRunDeps struct {
	initApp             func(asyncMCP bool, mode string, sessionOpts sessionStartupOptions) (*AppContext, error)
	createRuntime       func(*AppContext) (headlessRuntime, error)
	stdin               io.Reader
	stdout              io.Writer
	watchParent         bool
	parentCheckInterval time.Duration
	getppid             func() int
}

func defaultHeadlessRunDeps() headlessRunDeps {
	return headlessRunDeps{
		initApp: initApp,
		createRuntime: func(ac *AppContext) (headlessRuntime, error) {
			rt, err := createRuntime(ac)
			if err != nil {
				return nil, err
			}
			return runtimeHeadlessAdapter{rt: rt}, nil
		},
		stdin:               os.Stdin,
		stdout:              os.Stdout,
		watchParent:         true,
		parentCheckInterval: time.Second,
		getppid:             os.Getppid,
	}
}

// runHeadless executes the headless mode main loop.
func runHeadless(_ *cobra.Command, _ []string) error {
	return runHeadlessWithDeps(defaultHeadlessRunDeps())
}

func runHeadlessWithDeps(deps headlessRunDeps) error {
	if deps.initApp == nil {
		deps.initApp = defaultHeadlessRunDeps().initApp
	}
	if deps.createRuntime == nil {
		deps.createRuntime = defaultHeadlessRunDeps().createRuntime
	}
	if deps.stdin == nil {
		deps.stdin = os.Stdin
	}
	if deps.stdout == nil {
		deps.stdout = os.Stdout
	}
	if deps.getppid == nil {
		deps.getppid = os.Getppid
	}
	if deps.parentCheckInterval <= 0 {
		deps.parentCheckInterval = time.Second
	}

	ac, err := deps.initApp(false, "headless", sessionStartupOptions{
		ContinueLatest: flagContinueSession,
		ResumeID:       flagResumeSession,
		NewSessionMeta: flagWorktreeStartupMeta,
	})
	if err != nil {
		return err
	}
	if deps.watchParent {
		startHeadlessParentWatcher(ac, deps.getppid(), deps.parentCheckInterval, deps.getppid)
	}

	rt, err := deps.createRuntime(ac)
	if err != nil {
		ac.Close()
		return err
	}
	// Keep the writer alive through runtime shutdown so the event forwarder can
	// drain already-published events after the application context is cancelled.
	out := newStdoutWriter(context.Background(), deps.stdout)
	go out.run()

	sessionID := filepath.Base(ac.SessionDir)
	state := &headlessState{sessionID: sessionID, updatedAt: time.Now()}

	// Emit a one-time ready marker so gateways can detect successful init.
	readyPayload := map[string]any{
		"session_id": sessionID,
	}
	if len(ac.StartupSkippedLockedSessions) > 0 {
		readyPayload["skipped_locked_sessions"] = append([]string(nil), ac.StartupSkippedLockedSessions...)
	}
	if flagWorktreeStartupInfo != nil {
		readyPayload["worktree"] = map[string]any{
			"name":      flagWorktreeStartupInfo.Name,
			"branch":    flagWorktreeStartupInfo.Branch,
			"path":      flagWorktreeStartupInfo.Path,
			"repo_root": flagWorktreeStartupInfo.RepoRoot,
		}
	}
	out.emit(headlessEnvelope{
		Type:    "ready",
		Payload: readyPayload,
	})

	// Event loop: forward filtered events to stdout. Seq allocation and the
	// enqueue share one ordered section so event pushes keep wire order
	// aligned with seq order against command-path pushes.
	backend := rt.Backend()
	events := rt.Events()
	eventDone := make(chan struct{})
	go func() {
		defer close(eventDone)
		for ev := range events {
			out.ordered(func() {
				envs := filterHeadlessEvent(ev, state, backend)
				for _, env := range envs {
					out.emit(env)
				}
			})
		}
	}()
	defer func() {
		rt.Close()
		ac.Close()
		// Drain already-published events before closing stdout, but never hang
		// on a wedged event loop: the agent shutdown above got the same budget,
		// so a stuck channel must not keep headless from exiting.
		select {
		case <-eventDone:
		case <-time.After(agentShutdownWait):
			log.Warnf("headless event drain timed out")
		}
		out.close()
	}()

	// Command loop: read stdin JSON lines.
	stdinLines := make(chan headlessStdinLine, 16)
	go readHeadlessStdinLines(ac.Ctx, deps.stdin, stdinLines)

	for {
		select {
		case <-ac.Ctx.Done():
			return nil
		case item, ok := <-stdinLines:
			if !ok {
				// stdin closed (parent/gateway exited) → exit.
				ac.Cancel()
				return nil
			}
			if item.err != nil {
				payload := map[string]string{
					"message": "stdin read error: " + item.err.Error(),
				}
				if item.code != "" {
					payload["code"] = item.code
				}
				out.emit(headlessEnvelope{Type: "error", Payload: payload})
				if item.fatal {
					ac.Cancel()
					return nil
				}
				continue
			}

			line := item.line
			if len(line) == 0 {
				continue
			}

			var hcmd headlessCommand
			if err := json.Unmarshal(line, &hcmd); err != nil {
				out.emit(headlessEnvelope{
					Type: "error",
					Payload: map[string]string{
						"message": "invalid JSON command",
					},
				})
				continue
			}
			handleHeadlessCommand(hcmd, backend, state, out)
		}
	}
}

func readHeadlessStdinLines(ctx context.Context, r io.Reader, out chan<- headlessStdinLine) {
	defer close(out)
	reader := bufio.NewReaderSize(r, 64*1024)
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			line = append(line, chunk...)
			if len(line) > headlessStdinMaxLineBytes {
				discardHeadlessStdinLine(reader, err)
				line = nil
				if !sendHeadlessStdinLine(ctx, out, headlessStdinLine{code: "stdin_line_too_long", err: fmt.Errorf("line exceeds %d bytes", headlessStdinMaxLineBytes)}) {
					return
				}
				if errors.Is(err, io.EOF) {
					return
				}
				continue
			}
		}
		if err == nil {
			line = bytes.TrimSuffix(line, []byte("\n"))
			line = bytes.TrimSuffix(line, []byte("\r"))
			// line is reader-owned: the append above copies out of the bufio
			// buffer, and line is reset below, so ownership can move to the
			// consumer without a second copy.
			if !sendHeadlessStdinLine(ctx, out, headlessStdinLine{line: line}) {
				return
			}
			line = nil
			continue
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				line = bytes.TrimSuffix(line, []byte("\r"))
				_ = sendHeadlessStdinLine(ctx, out, headlessStdinLine{line: append([]byte(nil), line...)})
			}
			return
		}
		_ = sendHeadlessStdinLine(ctx, out, headlessStdinLine{err: err, fatal: true})
		return
	}
}

func discardHeadlessStdinLine(reader *bufio.Reader, err error) {
	for err == bufio.ErrBufferFull {
		_, err = reader.ReadSlice('\n')
	}
}

func sendHeadlessStdinLine(ctx context.Context, out chan<- headlessStdinLine, line headlessStdinLine) bool {
	select {
	case out <- line:
		return true
	case <-ctx.Done():
		return false
	}
}

func startHeadlessParentWatcher(ac *AppContext, ppid0 int, interval time.Duration, getppid func() int) {
	if ac == nil || getppid == nil {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ac.Ctx.Done():
				return
			case <-t.C:
				ppid := getppid()
				if ppid == 1 || (ppid0 != 0 && ppid != ppid0) {
					log.Warnf("parent process disappeared, exiting ppid0=%v ppid=%v", ppid0, ppid)
					ac.Cancel()
					return
				}
			}
		}
	}()
}

// headlessBackend is the subset of MainAgent functionality required by headless mode.
type headlessBackend interface {
	SendUserMessage(content string)
	CancelCurrentTurn() bool
	ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string)
	ResolveQuestion(answers []string, reason string, requestID string) (string, bool)
	// SupersedeQuestion closes a still-pending question because a newer user
	// message was accepted. It returns whether the request was still pending.
	SupersedeQuestion(requestID string) bool
}

type headlessHandoffBackend interface {
	HandoffAgentOptions() []agent.HandoffAgentOption
	SetAgentModelPool(agentName, pool string) error
	// ResolveHandoff delivers the user's decision (approve/deny/cancel) for the
	// pending handoff. The runtime settles the user-wait, emits the deferred
	// handoff tool result, and continues the flow (execute plan / reject /
	// cancel).
	ResolveHandoff(requestID, action, agentName, denyReason string)
}

type headlessModelsBackend interface {
	ModelsStatusText() string
	SetCurrentModelPool(pool string) error
}

type headlessBackendWithRuleIntent interface {
	ResolveConfirmWithRuleIntent(action, finalArgsJSON, editSummary, denyReason, requestID string, ruleIntent *agent.ConfirmRuleIntent)
}

func emitHeadlessModelsResponse(out *stdoutWriter, ok bool, message string, status string) {
	payload := map[string]any{"ok": ok}
	if message != "" {
		payload["message"] = message
	}
	if status != "" {
		payload["status"] = status
	}
	out.emit(headlessEnvelope{Type: "models_response", Payload: payload})
}

func handleHeadlessModelsCommand(cmd headlessCommand, backend headlessModelsBackend, out *stdoutWriter) {
	switch strings.TrimSpace(cmd.Action) {
	case "", "status":
		emitHeadlessModelsResponse(out, true, "", backend.ModelsStatusText())
	case "set_current_model_pool":
		pool := strings.TrimSpace(cmd.Pool)
		if pool == "" {
			emitHeadlessModelsResponse(out, false, "models set_current_model_pool requires pool", "")
			return
		}
		if err := backend.SetCurrentModelPool(pool); err != nil {
			emitHeadlessModelsResponse(out, false, err.Error(), "")
			return
		}
		emitHeadlessModelsResponse(out, true, "model pool set", backend.ModelsStatusText())
	default:
		emitHeadlessModelsResponse(out, false, "unsupported models action: "+cmd.Action, "")
	}
}

// headlessSessionBackend exposes the backend's committed session directory so
// session-switch pushes report the session the runtime actually runs, not a
// stale startup snapshot. MainAgent satisfies it.
type headlessSessionBackend interface {
	SessionDir() string
}

// headlessBackendSessionID resolves the active session id (directory base
// name, matching the ready/status_response convention), or "" when the backend
// cannot report one.
func headlessBackendSessionID(backend headlessBackend) string {
	sb, ok := backend.(headlessSessionBackend)
	if !ok {
		return ""
	}
	if dir := sb.SessionDir(); dir != "" {
		return filepath.Base(dir)
	}
	return ""
}

// headlessRoleBackend is the capability required by the role list/set commands.
// MainAgent satisfies it.
type headlessRoleBackend interface {
	AvailableRoles() []string
	CurrentRole() string
	SwitchRole(role string) error
}

type headlessRoleItem struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

// headlessRoleItems builds the ordered role list for a role_response, marking
// the backend's current role.
func headlessRoleItems(backend headlessRoleBackend) []headlessRoleItem {
	current := backend.CurrentRole()
	roles := backend.AvailableRoles()
	items := make([]headlessRoleItem, 0, len(roles))
	for _, name := range roles {
		items = append(items, headlessRoleItem{Name: name, Current: name == current})
	}
	return items
}

func emitHeadlessRoleResponse(out *stdoutWriter, ok bool, message string, role string, roles []headlessRoleItem) {
	payload := map[string]any{"ok": ok}
	if message != "" {
		payload["message"] = message
	}
	if role != "" {
		payload["role"] = role
	}
	if roles != nil {
		payload["roles"] = roles
	}
	out.emit(headlessEnvelope{Type: "role_response", Payload: payload})
}

// handleHeadlessRoleCommand serves the role list/set actions. A set mirrors the
// TUI Shift+Tab no-op protection (refusing the current role) and adds the
// headless-specific pending-handoff guard; target existence/availability is
// validated by the backend's SwitchRole so the "unknown role" and "not
// available (SubAgent-only)" answers live next to switchRole instead of being
// duplicated here.
func handleHeadlessRoleCommand(cmd headlessCommand, backend headlessRoleBackend, state *headlessState, out *stdoutWriter) {
	switch strings.TrimSpace(cmd.Action) {
	case "list":
		emitHeadlessRoleResponse(out, true, "", backend.CurrentRole(), headlessRoleItems(backend))
	case "set":
		role := strings.TrimSpace(cmd.Role)
		if role == "" {
			emitHeadlessRoleResponse(out, false, "role set requires role", "", nil)
			return
		}
		if role == backend.CurrentRole() {
			emitHeadlessRoleResponse(out, false, "already the active role: "+role, "", nil)
			return
		}
		state.mu.Lock()
		pendingHandoff := state.pendingHandoff
		state.mu.Unlock()
		if pendingHandoff != nil {
			emitHeadlessRoleResponse(out, false, "resolve the pending handoff before switching role", "", nil)
			return
		}
		// role set commits synchronously and takes effect immediately, even
		// while a turn is in flight — unlike a send message, which a busy agent
		// only processes at the next request boundary.
		if err := backend.SwitchRole(role); err != nil {
			emitHeadlessRoleResponse(out, false, err.Error(), "", nil)
			return
		}
		// Keep the state cache in sync with the backend immediately: the
		// RoleChangedEvent travels through the agent event loop and can trail
		// this response, which would leave status queries reading the previous
		// role from the warm cache. The committed switch is also announced here
		// (the response is a command reply, so the trailing event would
		// otherwise dedupe against the cache and the only role_change for the
		// switch would never be pushed); the marker update makes the trailing
		// event a no-op.
		emitHeadlessRoleResponse(out, true, "", role, headlessRoleItems(backend))
		// The cache write, seq allocation, and the role_change enqueue form one
		// ordered section: the announcement dedupe marker advances at stamp
		// time, so a push that reaches the wire out of seq order would be
		// dropped by version-comparing clients and never re-announced.
		out.ordered(func() {
			state.mu.Lock()
			state.role = role
			announce := state.lastBroadcastRole != role
			if announce {
				state.lastBroadcastRole = role
			}
			state.updatedAt = time.Now()
			var change *headlessEnvelope
			if announce {
				if state.isSubscribed("role_change") {
					change = &headlessEnvelope{Type: "role_change", Payload: map[string]string{
						"role": role,
					}}
					state.stampHeadlessSeq(true, change)
				} else {
					// The role cache moved even though no envelope is emitted:
					// bump so later status_response snapshots stay ordered.
					state.stampHeadlessSeq(true)
				}
			}
			state.mu.Unlock()
			if change != nil {
				out.emit(change)
			}
		})
	default:
		emitHeadlessRoleResponse(out, false, "unsupported role action: "+cmd.Action, "", nil)
	}
}

// headlessCurrentRole returns the state-cached active role, lazily filling it
// from the backend on first query. Startup chooses the role during session
// restore, which headlessState cannot know in advance, so the cache starts
// empty and converges on the first RoleChangedEvent or status query.
//
// The backfill deliberately does not bump seq: it only fills an empty cache,
// so no snapshot can have been copied "before" it (any snapshot either
// backfilled first or already saw the cached role), and the filled value is
// current at read time. Status re-reads state.role under the same lock as
// stampHeadlessSeq, so the returned value is not seq-consistent on its own —
// a RoleChangedEvent can bump seq after this returns.
func headlessCurrentRole(backend headlessBackend, state *headlessState) string {
	state.mu.Lock()
	role := state.role
	state.mu.Unlock()
	if role != "" {
		return role
	}
	roleBackend, ok := backend.(headlessRoleBackend)
	if !ok {
		return ""
	}
	role = roleBackend.CurrentRole()
	state.mu.Lock()
	if state.role == "" {
		state.role = role
	} else {
		role = state.role
	}
	state.mu.Unlock()
	return role
}

func emitHeadlessLocalShellResult(out *stdoutWriter, command, output string, err error) {
	payload := map[string]any{
		"command": command,
		"output":  output,
		"failed":  err != nil,
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	out.emit(headlessEnvelope{Type: "local_shell_result", Payload: payload})
}

// handleHeadlessCommand processes a single command from stdin.
func handleHeadlessCommand(cmd headlessCommand, backend headlessBackend, state *headlessState, out *stdoutWriter) {
	switch cmd.Type {
	case "subscribe":
		subs := make(map[string]bool, len(cmd.Events))
		for _, ev := range cmd.Events {
			if headlessEventTypes[ev] {
				subs[ev] = true
			}
		}
		state.mu.Lock()
		state.subscriptions = subs
		state.mu.Unlock()
		out.emit(headlessEnvelope{
			Type: "subscribe_response",
			Payload: map[string]any{
				"events": cmd.Events,
			},
		})

	case "status":
		// Fill an empty role cache before the snapshot. CurrentRole can block,
		// so it stays outside state.mu. The snapshot below re-reads state.role
		// under the same lock as seq so a concurrent RoleChangedEvent cannot
		// bind an older role to a newer version.
		headlessCurrentRole(backend, state)
		state.mu.Lock()
		currentRole := state.role
		sid := state.sessionID
		busy := state.busy
		phase := state.phase
		phaseDetail := state.phaseDetail
		pendingConfirm := state.pendingConfirm
		pendingQuestion := state.pendingQuestion
		pendingHandoff := state.pendingHandoff
		lastError := state.lastError
		lastOutcome := state.lastOutcome
		updatedAt := state.updatedAt
		seq := state.stampHeadlessSeq(false)
		state.mu.Unlock()
		out.emit(headlessEnvelope{
			Type: "status_response",
			Seq:  seq,
			Payload: map[string]any{
				"session_id":       sid,
				"busy":             busy,
				"phase":            phase,
				"phase_detail":     phaseDetail,
				"pending_confirm":  pendingConfirm,
				"pending_question": pendingQuestion,
				"pending_handoff":  pendingHandoff,
				"last_error":       lastError,
				"last_outcome":     lastOutcome,
				"current_role":     currentRole,
				"updated_at":       updatedAt.Format(time.RFC3339),
			},
		})

	case "local_shell":
		command := strings.TrimSpace(cmd.Command)
		if command == "" {
			command = strings.TrimSpace(cmd.Content)
		}
		if command == "" {
			emitHeadlessLocalShellResult(out, command, "", fmt.Errorf("empty local shell command"))
			return
		}
		output, err := tools.RunLocalShellCapture(context.Background(), "", command)
		emitHeadlessLocalShellResult(out, command, output, err)

	case "send":
		content := cmd.Content
		if strings.TrimSpace(content) == "/models" {
			content = "/models status"
		}
		if strings.TrimSpace(content) == "/role" {
			content = "/role status"
		}
		if isUnsupportedHeadlessCommand(content) {
			fields := strings.Fields(content)
			out.emit(headlessEnvelope{
				Type: "error",
				Payload: map[string]string{
					"message": fields[0] + " is only available in local TUI mode",
				},
			})
			return
		}
		// In headless mode, if a confirm_request, question_request, or handoff_request
		// is pending and the user sends a regular message (not /allow, /deny,
		// /answer, or handoff), auto-dismiss the pending interaction so the agent
		// can continue processing the new user message. Without this, the
		// interaction blocks forever (default timeout is 0 = infinite) and the user
		// message is queued but never consumed.
		state.mu.Lock()
		pendingConfirm := state.pendingConfirm
		pendingQuestion := state.pendingQuestion
		pendingHandoff := state.pendingHandoff
		state.mu.Unlock()
		if pendingConfirm != nil {
			log.Infof("headless: auto-denying pending confirm for new user message request_id=%v tool_name=%v", pendingConfirm.RequestID, pendingConfirm.ToolName)
			backend.ResolveConfirm("deny", "", "", "", pendingConfirm.RequestID)
			state.mu.Lock()
			if state.pendingConfirm != nil && state.pendingConfirm.RequestID == pendingConfirm.RequestID {
				state.pendingConfirm = nil
				state.updatedAt = time.Now()
				// No cancelled envelope exists for confirm, but the cache
				// change still needs a newer version so a status snapshot
				// copied while the request was pending cannot restore it.
				state.stampHeadlessSeq(true)
			}
			state.mu.Unlock()
		}
		if pendingHandoff != nil {
			log.Infof("headless: auto-cancelling pending handoff for new user message request_id=%v plan_path=%v", pendingHandoff.RequestID, pendingHandoff.PlanPath)
			if hb, ok := backend.(headlessHandoffBackend); ok {
				hb.ResolveHandoff(pendingHandoff.RequestID, "cancel", "", "")
			}
			// The client still holds this request as pending; tell subscribers it
			// is gone before the new message starts a fresh turn. The stamp and
			// the enqueue share one ordered section (see ordered), and the emit
			// inside it only ever waits on the channel buffer, so stdout
			// backpressure never stalls state.mu — only other push sections.
			out.ordered(func() {
				state.mu.Lock()
				cleared := false
				if state.pendingHandoff != nil && state.pendingHandoff.RequestID == pendingHandoff.RequestID {
					state.pendingHandoff = nil
					cleared = true
					state.updatedAt = time.Now()
				}
				var cancelled *headlessEnvelope
				if state.isSubscribed("handoff_cancelled") {
					cancelled = &headlessEnvelope{Type: "handoff_cancelled", Payload: map[string]string{
						"request_id": pendingHandoff.RequestID,
						"reason":     headlessHandoffCancelledReasonSuperseded,
					}}
				}
				if cleared || cancelled != nil {
					// Bump even without a cancelled envelope: the pending cache
					// changed, so a status snapshot copied earlier must not restore it.
					state.stampHeadlessSeq(true, cancelled)
				}
				state.mu.Unlock()
				if cancelled != nil {
					out.emit(cancelled)
				}
			})
		}
		if pendingQuestion != nil {
			// Accept the new message first, then wake the blocked Question as
			// superseded. SendUserMessage returns only once the message is
			// queued, so the resumed tool cannot reorder a model request ahead
			// of it. The pending cache is cleared by the core resolved event,
			// never locally. A handoff pending at the same time was cancelled
			// above, so neither interaction is left showing as pending.
			log.Infof("headless: superseding pending question for new user message request_id=%v tool_name=%v", pendingQuestion.RequestID, pendingQuestion.ToolName)
			backend.SendUserMessage(content)
			backend.SupersedeQuestion(pendingQuestion.RequestID)
			return
		}
		backend.SendUserMessage(content)

	case "models":
		modelsBackend, ok := backend.(headlessModelsBackend)
		if !ok {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "models command is not supported by this backend"}})
			return
		}
		handleHeadlessModelsCommand(cmd, modelsBackend, out)

	case "role":
		roleBackend, ok := backend.(headlessRoleBackend)
		if !ok {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "role command is not supported by this backend"}})
			return
		}
		handleHeadlessRoleCommand(cmd, roleBackend, state, out)

	case "handoff":
		handoffBackend, ok := backend.(headlessHandoffBackend)
		if !ok {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "handoff command is not supported by this backend"}})
			return
		}
		state.mu.Lock()
		pending := state.pendingHandoff
		state.mu.Unlock()
		if pending == nil || strings.TrimSpace(pending.RequestID) == "" || (cmd.RequestID != "" && cmd.RequestID != pending.RequestID) {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "no matching pending handoff"}})
			return
		}
		switch strings.TrimSpace(cmd.Action) {
		case "", "accept", "allow":
			agentName := strings.TrimSpace(cmd.Agent)
			if agentName == "" {
				agentName = defaultHandoffAgent(pending.Agents)
				if agentName == "" {
					out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "no eligible handoff agent"}})
					return
				}
			} else if !headlessHandoffAgentAvailable(pending.Agents, agentName) {
				out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "handoff target \"" + agentName + "\" is not available; choose one of the offered agents or cancel"}})
				return
			}
			if pool := strings.TrimSpace(cmd.Pool); pool != "" {
				if err := handoffBackend.SetAgentModelPool(agentName, pool); err != nil {
					out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": err.Error()}})
					return
				}
			}
			handoffBackend.ResolveHandoff(pending.RequestID, "approve", agentName, "")
		case "deny", "reject":
			reason := strings.TrimSpace(cmd.DenyReason)
			if reason == "" {
				reason = "Handoff rejected from headless client."
			}
			handoffBackend.ResolveHandoff(pending.RequestID, "deny", "", reason)
		case "cancel":
			handoffBackend.ResolveHandoff(pending.RequestID, "cancel", "", "")
		default:
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "unsupported handoff action: " + cmd.Action}})
			return
		}
		state.mu.Lock()
		if state.pendingHandoff != nil && state.pendingHandoff.RequestID == pending.RequestID {
			state.pendingHandoff = nil
			state.updatedAt = time.Now()
			state.stampHeadlessSeq(true)
		}
		state.mu.Unlock()

	case "confirm":
		ruleIntent, err := parseHeadlessRuleIntent(cmd.RulePattern, cmd.RuleScope)
		if err != nil {
			out.emit(headlessEnvelope{
				Type: "error",
				Payload: map[string]string{
					"message": err.Error(),
				},
			})
			return
		}
		if ruleIntent != nil {
			if withRuleIntent, ok := backend.(headlessBackendWithRuleIntent); ok {
				withRuleIntent.ResolveConfirmWithRuleIntent(cmd.Action, cmd.FinalArgsJSON, cmd.EditSummary, cmd.DenyReason, cmd.RequestID, ruleIntent)
			} else {
				backend.ResolveConfirm(cmd.Action, cmd.FinalArgsJSON, cmd.EditSummary, cmd.DenyReason, cmd.RequestID)
			}
		} else {
			backend.ResolveConfirm(cmd.Action, cmd.FinalArgsJSON, cmd.EditSummary, cmd.DenyReason, cmd.RequestID)
		}
		state.mu.Lock()
		if state.pendingConfirm != nil && state.pendingConfirm.RequestID == cmd.RequestID {
			state.pendingConfirm = nil
			state.updatedAt = time.Now()
			state.stampHeadlessSeq(true)
		}
		state.mu.Unlock()

	case "question":
		reason := strings.TrimSpace(cmd.Reason)
		if reason != tools.QuestionOutcomeAnswered && reason != tools.QuestionOutcomeDeclined {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{
				"message": "question reason must be answered or declined",
			}})
			return
		}
		// The core resolved event is the authoritative pending source, so the
		// cache is not cleared here: a rejected or late answer must not drop a
		// different pending question.
		terminal, accepted := backend.ResolveQuestion(cmd.Answers, reason, cmd.RequestID)
		if !accepted || terminal != reason {
			// The broker settles the terminal state under its lock, so a late
			// answer loses to the deadline instead of deciding the outcome.
			message := "question response was not accepted"
			if terminal != "" {
				message += ": the question closed as " + terminal
			}
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{
				"message": message,
			}})
			return
		}

	case "cancel":
		backend.CancelCurrentTurn()
		state.mu.Lock()
		state.pendingOutcome = "cancelled"
		state.updatedAt = time.Now()
		state.stampHeadlessSeq(true)
		state.mu.Unlock()

	default:
		out.emit(headlessEnvelope{
			Type: "error",
			Payload: map[string]string{
				"message": "unknown command type: " + cmd.Type,
			},
		})
	}
}

// headlessHandoffAgentAvailable reports whether name is one of the agents the
// pending handoff offered. External clients may only pick from the offered
// list; anything else (unknown role, SubAgent, or the current active role) is
// rejected before the runtime resolves the handoff.
func headlessHandoffAgentAvailable(options []agent.HandoffAgentOption, name string) bool {
	for _, opt := range options {
		if strings.TrimSpace(opt.Name) == name {
			return true
		}
	}
	return false
}

func defaultHandoffAgent(options []agent.HandoffAgentOption) string {
	for _, opt := range options {
		if opt.Default && strings.TrimSpace(opt.Name) != "" {
			return strings.TrimSpace(opt.Name)
		}
	}
	for _, opt := range options {
		if strings.TrimSpace(opt.Name) != "" {
			return strings.TrimSpace(opt.Name)
		}
	}
	// No eligible agent was offered: returning an empty name forces the caller
	// to report an error instead of inventing a target.
	return ""
}

func parseHeadlessRuleIntent(pattern, scope string) (*agent.ConfirmRuleIntent, error) {
	pattern = strings.TrimSpace(pattern)
	scope = strings.TrimSpace(scope)
	if pattern == "" && scope == "" {
		return nil, nil
	}
	if pattern == "" {
		return nil, fmt.Errorf("confirm rule intent requires non-empty rule_pattern")
	}
	if scope == "" {
		scope = "session"
	}
	var ruleScope int
	switch strings.ToLower(scope) {
	case "session":
		ruleScope = int(permission.ScopeSession)
	case "project":
		ruleScope = int(permission.ScopeProject)
	case "user_global", "user-global", "userglobal":
		ruleScope = int(permission.ScopeUserGlobal)
	default:
		return nil, fmt.Errorf("invalid rule_scope %q (expected session|project|user_global)", scope)
	}
	return &agent.ConfirmRuleIntent{
		Patterns: []string{pattern},
		Scope:    ruleScope,
	}, nil
}
