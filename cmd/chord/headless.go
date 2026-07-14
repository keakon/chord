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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

const (
	headlessLocalShellTimeout  = 120 * time.Second
	headlessLocalShellMaxBytes = 512 * 1024
	headlessStdinMaxLineBytes  = 1024 * 1024
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
}

type headlessQuestionPayload struct {
	ToolName      string   `json:"tool_name"`
	Header        string   `json:"header,omitempty"`
	Question      string   `json:"question"`
	Options       []string `json:"options"`
	OptionDetails []string `json:"option_details,omitempty"`
	DefaultAnswer string   `json:"default_answer"`
	Multiple      bool     `json:"multiple,omitempty"`
	RequestID     string   `json:"request_id,omitempty"`
	TimeoutMS     int64    `json:"timeout_ms,omitempty"`
}

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
	updatedAt       time.Time

	// subscriptions is the set of event types the gateway wants to receive.
	// If nil, no subscribe command has been received and all event types are
	// forwarded by default. An explicit empty map means the gateway subscribed
	// only to unknown/removed event types, so no optional events are forwarded.
	subscriptions map[string]bool
}

// isSubscribed returns true if the given event type should be forwarded.
func (s *headlessState) isSubscribed(eventType string) bool {
	if s.subscriptions == nil {
		return true // default before subscribe: all events
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
	enc       *json.Encoder
	ch        chan any
	ctx       context.Context
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
	"handoff_request":    true,
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
}

// filterHeadlessEvent converts an AgentEvent to one or more headlessEnvelopes.
// Returns nil if the event should be filtered out (not subscribed).
func filterHeadlessEvent(ev agent.AgentEvent, state *headlessState, backends ...headlessBackend) []*headlessEnvelope {
	var backend headlessBackend
	if len(backends) > 0 {
		backend = backends[0]
	}
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
		state.updatedAt = time.Now()
		if state.isSubscribed("agent_started") {
			out = append(out, &headlessEnvelope{Type: "agent_started", Payload: map[string]string{
				"agent_id":        e.AgentID,
				"task_id":         e.TaskID,
				"agent_type":      e.AgentType,
				"description":     e.Description,
				"parent_agent_id": e.ParentAgentID,
				"parent_task_id":  e.ParentTaskID,
			}})
		}
	case agent.AgentNotifyEvent:
		state.updatedAt = time.Now()
		if state.isSubscribed("agent_notify") {
			out = append(out, &headlessEnvelope{Type: "agent_notify", Payload: map[string]string{
				"agent_id":        e.AgentID,
				"task_id":         e.TaskID,
				"agent_type":      e.AgentType,
				"parent_agent_id": e.ParentAgentID,
				"parent_task_id":  e.ParentTaskID,
				"target_agent_id": e.TargetAgentID,
				"target_task_id":  e.TargetTaskID,
				"kind":            e.Kind,
				"message":         e.Message,
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
		doneReason, doneReport := parseHeadlessDoneArgs(e.ArgsJSON)
		if strings.TrimSpace(e.DoneReport) != "" {
			doneReport = strings.TrimSpace(e.DoneReport)
		}
		state.pendingConfirm = &headlessConfirmPayload{ToolName: e.ToolName, ArgsJSON: e.ArgsJSON, RequestID: e.RequestID, TimeoutMS: e.Timeout.Milliseconds(), NeedsApproval: e.NeedsApproval, AlreadyAllowed: e.AlreadyAllowed, NeedsApprovalRules: e.NeedsApprovalRules, AlreadyAllowedRules: e.AlreadyAllowedRules, DoneReport: doneReport, DoneReason: doneReason}
		state.updatedAt = time.Now()
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
			}})
		}
	case agent.QuestionRequestEvent:
		state.pendingQuestion = &headlessQuestionPayload{ToolName: e.ToolName, Header: e.Header, Question: e.Question, Options: e.Options, OptionDetails: e.OptionDetails, DefaultAnswer: e.DefaultAnswer, Multiple: e.Multiple, RequestID: e.RequestID, TimeoutMS: e.Timeout.Milliseconds()}
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
	case agent.HandoffEvent:
		payload := &headlessHandoffPayload{
			RequestID: fmt.Sprintf("handoff-%d", time.Now().UnixNano()),
			PlanPath:  e.PlanPath,
			Agents:    []agent.HandoffAgentOption{{Name: "builder", Default: true}},
		}
		if hb, ok := backend.(headlessHandoffBackend); ok {
			if options := hb.HandoffAgentOptions(); len(options) > 0 {
				payload.Agents = options
			}
		}
		if b, err := os.ReadFile(e.PlanPath); err == nil {
			payload.PlanText = string(b)
		} else {
			payload.PlanError = err.Error()
		}
		state.pendingHandoff = payload
		state.updatedAt = time.Now()
		if state.isSubscribed("handoff_request") {
			out = append(out, &headlessEnvelope{Type: "handoff_request", Payload: payload})
		}
	case agent.AgentDoneEvent:
		state.updatedAt = time.Now()
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
		state.updatedAt = time.Now()
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
  {"type":"models","action":"set_current_model_pool","pool":"thinking"}`,
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
				info, err := prepareStartupWorktree(wtCtx, flagHeadlessWorktree)
				if err != nil {
					return err
				}
				flagWorktreeStartupInfo = info
				flagWorktreeStartupMeta = worktreeMetaForInfo(info)
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
	cmd.Flags().StringVarP(&flagHeadlessWorktree, "worktree", "w", "", "Create or enter a chord-managed git worktree by name (auto-named when empty)")
	cmd.Flags().Lookup("worktree").NoOptDefVal = ""

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
	defer ac.Close()

	if deps.watchParent {
		startHeadlessParentWatcher(ac, deps.getppid(), deps.parentCheckInterval, deps.getppid)
	}

	rt, err := deps.createRuntime(ac)
	if err != nil {
		return err
	}
	defer rt.Close()

	out := newStdoutWriter(ac.Ctx, deps.stdout)
	go out.run()
	defer out.close()

	state := &headlessState{}
	state.updatedAt = time.Now()
	sessionID := filepath.Base(ac.SessionDir)

	// Emit a one-time ready marker so gateways can detect successful init.
	readyPayload := map[string]any{
		"session_id": sessionID,
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

	// Event loop: forward filtered events to stdout.
	backend := rt.Backend()
	events := rt.Events()
	go func() {
		for ev := range events {
			envs := filterHeadlessEvent(ev, state, backend)
			for _, env := range envs {
				out.emit(env)
			}
		}
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
			handleHeadlessCommand(hcmd, backend, state, out, sessionID)
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
			if !sendHeadlessStdinLine(ctx, out, headlessStdinLine{line: append([]byte(nil), line...)}) {
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
	ResolveQuestion(answers []string, cancelled bool, requestID string)
}

type headlessHandoffBackend interface {
	HandoffAgentOptions() []agent.HandoffAgentOption
	SetAgentModelPool(agentName, pool string) error
	ExecutePlan(planPath, agentName string)
	AppendContextMessage(msg message.Message)
	ContinueFromContext()
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

type headlessCappedWriter struct {
	buf      []byte
	total    int64
	maxBytes int64
}

func (c *headlessCappedWriter) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	if remaining := c.maxBytes - int64(len(c.buf)); remaining > 0 {
		if int64(len(p)) <= remaining {
			c.buf = append(c.buf, p...)
		} else {
			c.buf = append(c.buf, p[:remaining]...)
		}
	}
	return len(p), nil
}

func (c *headlessCappedWriter) String() string {
	s := string(c.buf)
	if c.total > c.maxBytes {
		s += fmt.Sprintf("\n...(output truncated: showed %d of %d bytes total)", len(c.buf), c.total)
	}
	return s
}

func runHeadlessLocalShell(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, headlessLocalShellTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	buf := &headlessCappedWriter{maxBytes: headlessLocalShellMaxBytes}
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	out := buf.String()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("timed out after %ds", int(headlessLocalShellTimeout/time.Second))
	}
	return out, err
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
				"pending_handoff":  state.pendingHandoff,
				"last_error":       state.lastError,
				"last_outcome":     state.lastOutcome,
				"updated_at":       state.updatedAt.Format(time.RFC3339),
			},
		})
		state.mu.Unlock()

	case "local_shell":
		command := strings.TrimSpace(cmd.Command)
		if command == "" {
			command = strings.TrimSpace(cmd.Content)
		}
		if command == "" {
			emitHeadlessLocalShellResult(out, command, "", fmt.Errorf("empty local shell command"))
			return
		}
		output, err := runHeadlessLocalShell(context.Background(), command)
		emitHeadlessLocalShellResult(out, command, output, err)

	case "send":
		content := cmd.Content
		if strings.TrimSpace(content) == "/models" {
			content = "/models status"
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
			}
			state.updatedAt = time.Now()
			state.mu.Unlock()
		}
		if pendingQuestion != nil {
			log.Infof("headless: auto-cancelling pending question for new user message request_id=%v tool_name=%v", pendingQuestion.RequestID, pendingQuestion.ToolName)
			backend.ResolveQuestion(nil, true, pendingQuestion.RequestID)
			state.mu.Lock()
			if state.pendingQuestion != nil && state.pendingQuestion.RequestID == pendingQuestion.RequestID {
				state.pendingQuestion = nil
			}
			state.updatedAt = time.Now()
			state.mu.Unlock()
		}
		if pendingHandoff != nil {
			log.Infof("headless: auto-cancelling pending handoff for new user message request_id=%v plan_path=%v", pendingHandoff.RequestID, pendingHandoff.PlanPath)
			state.mu.Lock()
			if state.pendingHandoff != nil && state.pendingHandoff.RequestID == pendingHandoff.RequestID {
				state.pendingHandoff = nil
			}
			state.updatedAt = time.Now()
			state.mu.Unlock()
		}
		backend.SendUserMessage(content)

	case "models":
		modelsBackend, ok := backend.(headlessModelsBackend)
		if !ok {
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "models command is not supported by this backend"}})
			return
		}
		handleHeadlessModelsCommand(cmd, modelsBackend, out)

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
			}
			if pool := strings.TrimSpace(cmd.Pool); pool != "" {
				if err := handoffBackend.SetAgentModelPool(agentName, pool); err != nil {
					out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": err.Error()}})
					return
				}
			}
			handoffBackend.ExecutePlan(pending.PlanPath, agentName)
		case "deny", "reject", "cancel":
			reason := strings.TrimSpace(cmd.DenyReason)
			if reason == "" {
				reason = "Handoff rejected from headless client."
			}
			handoffBackend.AppendContextMessage(message.Message{Role: "user", Content: fmt.Sprintf("Handoff rejected: %s\n\nPlan path: %s", reason, pending.PlanPath)})
			handoffBackend.ContinueFromContext()
		default:
			out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "unsupported handoff action: " + cmd.Action}})
			return
		}
		state.mu.Lock()
		if state.pendingHandoff != nil && state.pendingHandoff.RequestID == pending.RequestID {
			state.pendingHandoff = nil
		}
		state.updatedAt = time.Now()
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
	return "builder"
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
