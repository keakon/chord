// Package main provides the chord headless subcommand for stdio JSON protocol mode.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/protocol"
)

// headlessState holds mutex-protected state for the headless protocol.
type headlessState struct {
	mu              sync.Mutex
	busy            bool
	phase           string
	phaseDetail     string
	pendingConfirm  *protocol.ConfirmRequestPayload
	pendingQuestion *protocol.QuestionRequestPayload
	lastError       string
	pendingOutcome  string // "completed" / "cancelled" / "error" / ""
	lastOutcome     string // persists across idle; set from pendingOutcome on idle
	updatedAt       time.Time

	// subscriptions is the set of event types the gateway wants to receive.
	// If nil or empty, all event types are forwarded (default: all).
	subscriptions map[string]bool
}

// isSubscribed returns true if the given event type should be forwarded.
func (s *headlessState) isSubscribed(eventType string) bool {
	if len(s.subscriptions) == 0 {
		return true // default: all events
	}
	return s.subscriptions[eventType]
}

// headlessEnvelope is the JSON envelope for stdio protocol messages.
type headlessEnvelope struct {
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

// stdoutWriter serializes JSON envelopes to stdout via a single goroutine.
type stdoutWriter struct {
	enc *json.Encoder
	ch  chan any
	ctx context.Context
}

// newStdoutWriter creates a stdoutWriter with a buffered channel.
func newStdoutWriter(ctx context.Context, w io.Writer) *stdoutWriter {
	return &stdoutWriter{
		enc: json.NewEncoder(w),
		ch:  make(chan any, 256),
		ctx: ctx,
	}
}

// run processes the channel and writes JSON envelopes to stdout.
func (w *stdoutWriter) run() {
	for {
		select {
		case <-w.ctx.Done():
			return
		case msg, ok := <-w.ch:
			if !ok {
				return
			}
			_ = w.enc.Encode(msg)
		}
	}
}

// emit sends a message to the channel. It blocks if the channel is full
// (stdio is a reliable pipe; confirm_request, question_request, and
// response events must never be silently dropped).
// Returns false if the context was cancelled before the message could be sent.
func (w *stdoutWriter) emit(msg any) bool {
	select {
	case w.ch <- msg:
		return true
	case <-w.ctx.Done():
		return false
	}
}

// headlessCommand represents a command received from stdin.
type headlessCommand struct {
	Type          string   `json:"type"`
	Content       string   `json:"content,omitempty"`
	RequestID     string   `json:"request_id,omitempty"`
	Action        string   `json:"action,omitempty"`
	FinalArgsJSON string   `json:"final_args_json,omitempty"`
	EditSummary   string   `json:"edit_summary,omitempty"`
	DenyReason    string   `json:"deny_reason,omitempty"`
	RulePattern   string   `json:"rule_pattern,omitempty"`
	RuleScope     string   `json:"rule_scope,omitempty"` // session | project | user_global
	Answers       []string `json:"answers,omitempty"`
	Cancelled     bool     `json:"cancelled,omitempty"`
	Events        []string `json:"events,omitempty"` // for subscribe command
}

// All available push event types that can be subscribed to.
var headlessEventTypes = map[string]bool{
	"activity":           true,
	"assistant_message":  true,
	"idle":               true,
	"confirm_request":    true,
	"question_request":   true,
	"error":              true,
	"agent_done":         true,
	"info":               true,
	"toast":              true,
	"tool_result":        true,
	"assistant_rollback": true,
	"notification":       true,
	"todos":              true,
}

// filterHeadlessEvent converts an AgentEvent to one or more headlessEnvelopes.
// Returns nil if the event should be filtered out (not subscribed).
func filterHeadlessEvent(ev agent.AgentEvent, state *headlessState) []*headlessEnvelope {
	state.mu.Lock()
	defer state.mu.Unlock()

	var out []*headlessEnvelope

	switch e := ev.(type) {
	case agent.AgentActivityEvent:
		if e.Type == agent.ActivityIdle {
			return nil // filtered; idle is expressed via IdleEvent
		}
		state.busy = true
		state.phase = string(e.Type)
		state.phaseDetail = e.Detail
		state.updatedAt = time.Now()
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
	case agent.AssistantMessageEvent:
		state.updatedAt = time.Now()
		if strings.TrimSpace(e.Text) == "" {
			slog.Warn("headless observed empty assistant_message", "agent_id", e.AgentID, "tool_calls", e.ToolCalls)
		} else {
			slog.Debug("headless forwarding assistant_message", "agent_id", e.AgentID, "text_len", len(e.Text), "tool_calls", e.ToolCalls)
		}
		if state.isSubscribed("assistant_message") {
			out = append(out, &headlessEnvelope{Type: "assistant_message", Payload: map[string]any{
				"agent_id":   e.AgentID,
				"text":       e.Text,
				"tool_calls": e.ToolCalls,
			}})
		}
	case agent.IdleEvent:
		state.busy = false
		state.phase = ""
		state.phaseDetail = ""
		outcome := state.pendingOutcome
		state.lastOutcome = outcome
		state.pendingOutcome = ""
		state.pendingConfirm = nil
		state.pendingQuestion = nil
		state.lastError = ""
		state.updatedAt = time.Now()
		if state.isSubscribed("idle") {
			out = append(out, &headlessEnvelope{Type: "idle", Payload: map[string]any{"last_outcome": outcome}})
		}
	case agent.ErrorEvent:
		state.pendingOutcome = "error"
		if e.Err != nil {
			state.lastError = e.Err.Error()
		}
		state.updatedAt = time.Now()
		if state.isSubscribed("error") {
			out = append(out, &headlessEnvelope{Type: "error", Payload: map[string]string{
				"message":  state.lastError,
				"agent_id": e.AgentID,
			}})
		}
	case agent.ConfirmRequestEvent:
		state.pendingConfirm = &protocol.ConfirmRequestPayload{ToolName: e.ToolName, ArgsJSON: e.ArgsJSON, RequestID: e.RequestID, TimeoutMS: e.Timeout.Milliseconds(), NeedsApproval: e.NeedsApproval, AlreadyAllowed: e.AlreadyAllowed}
		state.updatedAt = time.Now()
		if state.isSubscribed("confirm_request") {
			out = append(out, &headlessEnvelope{Type: "confirm_request", Payload: map[string]any{
				"tool_name":       e.ToolName,
				"args_json":       e.ArgsJSON,
				"request_id":      e.RequestID,
				"timeout_ms":      e.Timeout.Milliseconds(),
				"needs_approval":  e.NeedsApproval,
				"already_allowed": e.AlreadyAllowed,
			}})
		}
	case agent.QuestionRequestEvent:
		state.pendingQuestion = &protocol.QuestionRequestPayload{ToolName: e.ToolName, Header: e.Header, Question: e.Question, Options: e.Options, OptionDetails: e.OptionDetails, DefaultAnswer: e.DefaultAnswer, Multiple: e.Multiple, RequestID: e.RequestID, TimeoutMS: e.Timeout.Milliseconds()}
		state.updatedAt = time.Now()
		if state.isSubscribed("question_request") {
			out = append(out, &headlessEnvelope{Type: "question_request", Payload: map[string]any{
				"tool_name":      e.ToolName,
				"header":         e.Header,
				"question":       e.Question,
				"options":        e.Options,
				"option_details": e.OptionDetails,
				"default_answer": e.DefaultAnswer,
				"multiple":       e.Multiple,
				"request_id":     e.RequestID,
				"timeout_ms":     e.Timeout.Milliseconds(),
			}})
		}
	case agent.AgentDoneEvent:
		if state.isSubscribed("agent_done") {
			out = append(out, &headlessEnvelope{Type: "agent_done", Payload: map[string]string{"agent_id": e.AgentID, "task_id": e.TaskID, "summary": e.Summary}})
		}
	case agent.InfoEvent:
		if state.isSubscribed("info") {
			out = append(out, &headlessEnvelope{Type: "info", Payload: map[string]string{"message": e.Message, "agent_id": e.AgentID}})
		}
	case agent.ToolResultEvent:
		state.updatedAt = time.Now()
		if state.isSubscribed("tool_result") {
			out = append(out, &headlessEnvelope{Type: "tool_result", Payload: map[string]string{"call_id": e.CallID, "name": e.Name, "status": string(e.Status), "agent_id": e.AgentID}})
		}
	case agent.StreamRollbackEvent:
		state.updatedAt = time.Now()
		if state.isSubscribed("assistant_rollback") {
			out = append(out, &headlessEnvelope{Type: "assistant_rollback", Payload: map[string]string{"reason": e.Reason, "agent_id": e.AgentID}})
		}
	case agent.TodosUpdatedEvent:
		state.updatedAt = time.Now()
		if state.isSubscribed("todos") {
			out = append(out, &headlessEnvelope{Type: "todos", Payload: map[string]any{"todos": e.Todos}})
		}
	case agent.ToastEvent:
		if state.isSubscribed("toast") {
			out = append(out, &headlessEnvelope{Type: "toast", Payload: map[string]string{"message": e.Message, "level": e.Level, "agent_id": e.AgentID}})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isUnsupportedHeadlessCommand checks if a command is only available in TUI mode.
func isUnsupportedHeadlessCommand(content string) bool {
	fields := strings.Fields(strings.TrimSpace(content))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/model", "/export":
		return true
	case "/resume":
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
  chord headless --resume <session-id>`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flagHeadlessDir != "" {
				if err := os.Chdir(flagHeadlessDir); err != nil {
					return fmt.Errorf("change to session-dir %q: %w", flagHeadlessDir, err)
				}
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
			return runHeadless(cmd, nil)
		},
	}

	cmd.Flags().StringVarP(&flagHeadlessDir, "session-dir", "d", "", "Project directory (session directory)")
	cmd.Flags().BoolVarP(&flagHeadlessContinue, "continue", "c", false, "Continue the latest session")
	cmd.Flags().StringVarP(&flagHeadlessResume, "resume", "r", "", "Resume a specific session ID")

	return cmd
}

// runHeadless executes the headless mode main loop.
func runHeadless(_ *cobra.Command, _ []string) error {
	ac, err := initApp(false, "headless", sessionStartupOptions{
		ContinueLatest: flagContinueSession,
		ResumeID:       flagResumeSession,
	})
	if err != nil {
		return err
	}
	defer ac.Close()

	// Parent-death watcher: if gateway dies (e.g. SIGKILL), this process may be
	// reparented. Exit promptly to avoid leaving orphaned session locks.
	ppid0 := os.Getppid()
	go func() {
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ac.Ctx.Done():
				return
			case <-t.C:
				ppid := os.Getppid()
				if ppid == 1 || (ppid0 != 0 && ppid != ppid0) {
					slog.Warn("parent process disappeared, exiting", "ppid0", ppid0, "ppid", ppid)
					ac.Cancel()
					return
				}
			}
		}
	}()

	rt, err := createRuntime(ac)
	if err != nil {
		return err
	}
	defer rt.Close()

	out := newStdoutWriter(ac.Ctx, os.Stdout)
	go out.run()

	state := &headlessState{}
	state.updatedAt = time.Now()
	sessionID := filepath.Base(ac.SessionDir)

	// Emit a one-time ready marker so gateways can detect successful init.
	out.emit(headlessEnvelope{
		Type: "ready",
		Payload: map[string]any{
			"session_id": sessionID,
		},
	})

	// Event loop: forward filtered events to stdout
	events := rt.Agent.Events()
	go func() {
		for ev := range events {
			envs := filterHeadlessEvent(ev, state)
			for _, env := range envs {
				out.emit(env)
			}
		}
	}()

	// Command loop: read stdin JSON lines
	lines := make(chan []byte, 16)
	scanErr := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			b := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- b:
			case <-ac.Ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			scanErr <- err
		}
	}()

	for {
		select {
		case <-ac.Ctx.Done():
			return nil
		case err := <-scanErr:
			out.emit(headlessEnvelope{
				Type: "error",
				Payload: map[string]string{
					"message": "stdin read error: " + err.Error(),
				},
			})
			ac.Cancel()
			return nil
		case line, ok := <-lines:
			if !ok {
				// stdin closed (parent/gateway exited) → exit
				ac.Cancel()
				return nil
			}

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
			handleHeadlessCommand(hcmd, rt.Agent, state, out, sessionID)
		}
	}
}

// headlessBackend is the subset of MainAgent functionality required by headless mode.
type headlessBackend interface {
	SendUserMessage(content string)
	CancelCurrentTurn() bool
	ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string)
	ResolveQuestion(answers []string, cancelled bool, requestID string)
}

type headlessBackendWithRuleIntent interface {
	ResolveConfirmWithRuleIntent(action, finalArgsJSON, editSummary, denyReason, requestID string, ruleIntent *agent.ConfirmRuleIntent)
}

// handleHeadlessCommand processes a single command from stdin.
func handleHeadlessCommand(cmd headlessCommand, backend headlessBackend, state *headlessState, out *stdoutWriter, sessionID string) {
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
		state.mu.Lock()
		out.emit(headlessEnvelope{
			Type: "status_response",
			Payload: map[string]any{
				"session_id":       sessionID,
				"busy":             state.busy,
				"phase":            state.phase,
				"phase_detail":     state.phaseDetail,
				"pending_confirm":  state.pendingConfirm,
				"pending_question": state.pendingQuestion,
				"last_error":       state.lastError,
				"last_outcome":     state.lastOutcome,
				"updated_at":       state.updatedAt.Format(time.RFC3339),
			},
		})
		state.mu.Unlock()

	case "send":
		if isUnsupportedHeadlessCommand(cmd.Content) {
			fields := strings.Fields(cmd.Content)
			out.emit(headlessEnvelope{
				Type: "error",
				Payload: map[string]string{
					"message": fields[0] + " is only available in local TUI mode",
				},
			})
			return
		}
		// In headless mode, if a confirm_request or question_request is pending
		// and the user sends a regular message (not /allow, /deny, or /answer),
		// auto-dismiss the pending interaction so the agent can continue
		// processing the new user message. Without this, the interaction
		// blocks forever (default timeout is 0 = infinite) and the user
		// message is queued but never consumed.
		state.mu.Lock()
		pendingConfirm := state.pendingConfirm
		pendingQuestion := state.pendingQuestion
		state.mu.Unlock()
		if pendingConfirm != nil {
			slog.Info("headless: auto-denying pending confirm for new user message",
				"request_id", pendingConfirm.RequestID,
				"tool_name", pendingConfirm.ToolName,
			)
			backend.ResolveConfirm("deny", "", "", "", pendingConfirm.RequestID)
			state.mu.Lock()
			if state.pendingConfirm != nil && state.pendingConfirm.RequestID == pendingConfirm.RequestID {
				state.pendingConfirm = nil
			}
			state.updatedAt = time.Now()
			state.mu.Unlock()
		}
		if pendingQuestion != nil {
			slog.Info("headless: auto-cancelling pending question for new user message",
				"request_id", pendingQuestion.RequestID,
				"tool_name", pendingQuestion.ToolName,
			)
			backend.ResolveQuestion(nil, true, pendingQuestion.RequestID)
			state.mu.Lock()
			if state.pendingQuestion != nil && state.pendingQuestion.RequestID == pendingQuestion.RequestID {
				state.pendingQuestion = nil
			}
			state.updatedAt = time.Now()
			state.mu.Unlock()
		}
		backend.SendUserMessage(cmd.Content)

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
		}
		state.updatedAt = time.Now()
		state.mu.Unlock()

	case "question":
		backend.ResolveQuestion(cmd.Answers, cmd.Cancelled, cmd.RequestID)
		state.mu.Lock()
		if state.pendingQuestion != nil && state.pendingQuestion.RequestID == cmd.RequestID {
			state.pendingQuestion = nil
		}
		state.updatedAt = time.Now()
		state.mu.Unlock()

	case "cancel":
		backend.CancelCurrentTurn()
		state.mu.Lock()
		state.pendingOutcome = "cancelled"
		state.updatedAt = time.Now()
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
		Pattern: pattern,
		Scope:   ruleScope,
	}, nil
}
