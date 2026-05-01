package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/keakon/golog/log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	OnToolCall               = "on_tool_call"
	OnToolResult             = "on_tool_result"
	OnBeforeToolResultAppend = "on_before_tool_result_append"
	OnBeforeLLMCall          = "on_before_llm_call"
	OnAfterLLMCall           = "on_after_llm_call"
	OnBeforeCompress         = "on_before_compress"
	OnAfterCompress          = "on_after_compress"
	OnSessionStart           = "on_session_start"
	OnSessionEnd             = "on_session_end"
	OnIdle                   = "on_idle"
	OnWaitConfirm            = "on_wait_confirm"
	OnWaitQuestion           = "on_wait_question"
	OnAgentError             = "on_agent_error"
	OnToolBatchComplete      = "on_tool_batch_complete"
)

const DefaultTimeout = 30

const (
	ActionContinue = "continue"
	ActionBlock    = "block"
	ActionModify   = "modify"
)

const (
	JoinBackground    = "background"
	JoinBeforeNextLLM = "before_next_llm"
)

const (
	ResultIgnore          = "ignore"
	ResultNotifyOnly      = "notify_only"
	ResultAppendOnFailure = "append_on_failure"
	ResultAlwaysAppend    = "always_append"
)

const (
	ResultFormatSummary = "summary"
	ResultFormatTail    = "tail"
	ResultFormatFull    = "full"
)

const (
	AutomationStatusSuccess = "success"
	AutomationStatusFailed  = "failed"
)

const (
	statusExecuted = "executed"
	statusSkipped  = "skipped"
	statusFailed   = "failed"
	statusTimedOut = "timed_out"
)

type category string

const (
	categorySync       category = "sync"
	categoryObserver   category = "observer"
	categoryAutomation category = "automation"
)

// Command specifies how a hook should be executed.
// Exactly one of Shell or Args should be set.
type Command struct {
	Shell string
	Args  []string
}

func (c Command) IsZero() bool {
	return c.Shell == "" && len(c.Args) == 0
}

func (c Command) mode() string {
	if len(c.Args) > 0 {
		return "argv"
	}
	return "shell"
}

// HookDef defines one hook entry after config parsing.
type HookDef struct {
	Name            string
	Point           string
	Command         Command
	Timeout         int
	Tools           []string
	Paths           []string
	Agents          []string
	AgentKinds      []string
	Models          []string
	MinChangedFiles int
	OnlyOnError     bool
	Join            string
	Result          string
	ResultFormat    string
	MaxResultLines  int
	MaxResultBytes  int
	DebounceMS      int
	Concurrency     string
	RetryOnFailure  int
	RetryDelayMS    int
	Environment     map[string]string
}

// Envelope is the canonical hook payload format.
type Envelope struct {
	Point         string    `json:"point"`
	Timestamp     time.Time `json:"timestamp"`
	SessionID     string    `json:"session_id"`
	TurnID        uint64    `json:"turn_id,omitempty"`
	AgentID       string    `json:"agent_id"`
	AgentKind     string    `json:"agent_kind"`
	ProjectRoot   string    `json:"project_root,omitempty"`
	SelectedModel string    `json:"selected_model,omitempty"`
	RunningModel  string    `json:"running_model,omitempty"`
	Data          any       `json:"data,omitempty"`
}

// Result is the outcome of a synchronous interceptor hook.
type Result struct {
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// AutomationResult is the stdout contract for automation hooks.
type AutomationResult struct {
	Status        string `json:"status,omitempty"`
	Summary       string `json:"summary,omitempty"`
	Body          string `json:"body,omitempty"`
	Severity      string `json:"severity,omitempty"`
	AppendContext bool   `json:"append_context,omitempty"`
	Notify        bool   `json:"notify,omitempty"`
}

// AutomationJobResult is one completed joinable automation hook run.
type AutomationJobResult struct {
	Hook   HookDef
	Result AutomationResult
}

// Manager is the hook runtime interface used by agents.
type Manager interface {
	Fire(ctx context.Context, env Envelope) (*Result, error)
	FireBackground(ctx context.Context, env Envelope)
	RunAutomation(ctx context.Context, env Envelope) ([]AutomationJobResult, error)
}

// NoopEngine is used when no hooks are configured.
type NoopEngine struct{}

func (e *NoopEngine) Fire(_ context.Context, _ Envelope) (*Result, error) {
	return &Result{Action: ActionContinue}, nil
}

func (e *NoopEngine) FireBackground(_ context.Context, _ Envelope) {}

func (e *NoopEngine) RunAutomation(_ context.Context, _ Envelope) ([]AutomationJobResult, error) {
	return nil, nil
}

// CommandEngine executes shell-based hooks.
type CommandEngine struct {
	hooks map[string][]HookDef
}

func NewCommandEngine(hooks map[string][]HookDef) *CommandEngine {
	if hooks == nil {
		hooks = make(map[string][]HookDef)
	}
	return &CommandEngine{hooks: hooks}
}

func NewCommandEngineFromList(defs []HookDef) *CommandEngine {
	hooks := make(map[string][]HookDef)
	for _, d := range defs {
		if d.Command.IsZero() {
			continue
		}
		if d.Name == "" {
			d.Name = d.Point
		}
		hooks[d.Point] = append(hooks[d.Point], d)
	}
	return &CommandEngine{hooks: hooks}
}

func (e *CommandEngine) Fire(ctx context.Context, env Envelope) (*Result, error) {
	hooks := e.hooks[env.Point]
	if len(hooks) == 0 {
		return &Result{Action: ActionContinue}, nil
	}

	switch pointCategory(env.Point) {
	case categoryAutomation:
		_, err := e.RunAutomation(ctx, env)
		return &Result{Action: ActionContinue}, err
	case categoryObserver:
		e.fireObserver(ctx, env, hooks)
		return &Result{Action: ActionContinue}, nil
	default:
		return e.fireSync(ctx, env, hooks)
	}
}

func (e *CommandEngine) FireBackground(ctx context.Context, env Envelope) {
	go func() {
		if _, err := e.Fire(ctx, env); err != nil {
			log.Warnf("background hook execution failed point=%v error=%v", env.Point, err)
		}
	}()
}

func (e *CommandEngine) RunAutomation(ctx context.Context, env Envelope) ([]AutomationJobResult, error) {
	hooks := e.hooks[env.Point]
	if len(hooks) == 0 {
		return nil, nil
	}

	results := make([]AutomationJobResult, 0, len(hooks))
	for _, h := range hooks {
		if ok, reason := shouldRunHook(h, env); !ok {
			logHook(h, env, statusSkipped, "", 0, reason, nil)
			continue
		}

		if normalizeJoin(h.Join) == JoinBackground {
			hookDef := h
			go func() {
				if _, err := e.runAutomationHook(ctx, env, hookDef); err != nil {
					log.Warnf("background automation hook failed point=%v hook_name=%v error=%v", env.Point, hookDef.Name, err)
				}
			}()
			continue
		}

		result, err := e.runAutomationHook(ctx, env, h)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}

	return results, nil
}

func (e *CommandEngine) fireSync(ctx context.Context, env Envelope, hooks []HookDef) (*Result, error) {
	modified := false
	data := env.Data

	for _, h := range hooks {
		env.Data = data
		if ok, reason := shouldRunHook(h, env); !ok {
			logHook(h, env, statusSkipped, "", 0, reason, nil)
			continue
		}

		output, duration, timedOut, err := executeHook(ctx, h, env)
		if err != nil {
			status := statusFailed
			if timedOut {
				status = statusTimedOut
			}
			logHook(h, env, status, "", duration, "", err)
			continue
		}

		result, parseErr := parseSyncResult(output)
		if parseErr != nil {
			logHook(h, env, statusFailed, "", duration, "", parseErr)
			continue
		}
		logHook(h, env, statusExecuted, result.Action, duration, "", nil)

		switch result.Action {
		case ActionBlock:
			return result, nil
		case ActionModify:
			if result.Data != nil {
				data = result.Data
				modified = true
			}
		case "", ActionContinue:
		default:
			log.Warnf("unknown hook action, treating as continue point=%v hook_name=%v action=%v", env.Point, h.Name, result.Action)
		}
	}

	result := &Result{Action: ActionContinue}
	if modified {
		result.Data = data
	}
	return result, nil
}

func (e *CommandEngine) fireObserver(ctx context.Context, env Envelope, hooks []HookDef) {
	for _, h := range hooks {
		if ok, reason := shouldRunHook(h, env); !ok {
			logHook(h, env, statusSkipped, "", 0, reason, nil)
			continue
		}

		output, duration, timedOut, err := executeHook(ctx, h, env)
		if err != nil {
			status := statusFailed
			if timedOut {
				status = statusTimedOut
			}
			logHook(h, env, status, "", duration, "", err)
			continue
		}

		trimmed := strings.TrimSpace(string(output))
		logHook(h, env, statusExecuted, trimmed, duration, "", nil)
	}
}

func (e *CommandEngine) runAutomationHook(ctx context.Context, env Envelope, h HookDef) (AutomationJobResult, error) {
	retries := h.RetryOnFailure
	for attempt := 0; ; attempt++ {
		output, duration, timedOut, err := executeHook(ctx, h, env)
		if err != nil {
			status := statusFailed
			if timedOut {
				status = statusTimedOut
			}
			logHook(h, env, status, "", duration, "", err)
			if attempt >= retries {
				return AutomationJobResult{}, err
			}
			delay := time.Duration(h.RetryDelayMS) * time.Millisecond
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return AutomationJobResult{}, ctx.Err()
				}
			}
			continue
		}

		result, parseErr := parseAutomationResult(output)
		if parseErr != nil {
			logHook(h, env, statusFailed, "", duration, "", parseErr)
			return AutomationJobResult{}, parseErr
		}
		logHook(h, env, statusExecuted, result.Status, duration, "", nil)
		return AutomationJobResult{Hook: normalizeHookDefaults(h), Result: result}, nil
	}
}

func pointCategory(point string) category {
	switch point {
	case OnToolCall, OnBeforeLLMCall, OnBeforeToolResultAppend:
		return categorySync
	case OnToolBatchComplete:
		return categoryAutomation
	default:
		return categoryObserver
	}
}

func parseSyncResult(output []byte) (*Result, error) {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return &Result{Action: ActionContinue}, nil
	}

	var result Result
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parse hook sync result: %w", err)
	}
	if result.Action == "" {
		result.Action = ActionContinue
	}
	return &result, nil
}

func parseAutomationResult(output []byte) (AutomationResult, error) {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return AutomationResult{
			Status:   AutomationStatusSuccess,
			Severity: "info",
		}, nil
	}

	var result AutomationResult
	if err := json.Unmarshal(output, &result); err != nil {
		return AutomationResult{}, fmt.Errorf("parse automation hook result: %w", err)
	}
	if result.Status == "" {
		result.Status = AutomationStatusSuccess
	}
	if result.Severity == "" {
		if result.Status == AutomationStatusFailed {
			result.Severity = "error"
		} else {
			result.Severity = "info"
		}
	}
	return result, nil
}

func normalizeHookDefaults(h HookDef) HookDef {
	if h.Name == "" {
		h.Name = h.Point
	}
	if h.Join == "" {
		h.Join = JoinBackground
	}
	if h.ResultFormat == "" {
		h.ResultFormat = ResultFormatSummary
	}
	if h.MaxResultLines <= 0 {
		h.MaxResultLines = 50
	}
	if h.MaxResultBytes <= 0 {
		h.MaxResultBytes = 4096
	}
	return h
}

func normalizeJoin(join string) string {
	if join == JoinBeforeNextLLM {
		return JoinBeforeNextLLM
	}
	return JoinBackground
}

func executeHook(ctx context.Context, h HookDef, env Envelope) ([]byte, time.Duration, bool, error) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	hookCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd, err := buildCommand(hookCtx, h.Command)
	if err != nil {
		return nil, 0, false, err
	}
	if env.ProjectRoot != "" {
		cmd.Dir = env.ProjectRoot
	}
	cmd.Env = append(os.Environ(), buildHookEnv(env, h)...)

	inputJSON, err := json.Marshal(env)
	if err != nil {
		return nil, 0, false, fmt.Errorf("marshal hook envelope: %w", err)
	}
	cmd.Stdin = bytes.NewReader(inputJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)
	timedOut := errors.Is(hookCtx.Err(), context.DeadlineExceeded)
	if runErr != nil {
		return stdout.Bytes(), duration, timedOut, fmt.Errorf("hook %q failed: %w (stderr: %s)",
			h.Name, runErr, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), duration, false, nil
}

func buildCommand(ctx context.Context, cmdSpec Command) (*exec.Cmd, error) {
	if len(cmdSpec.Args) > 0 {
		return exec.CommandContext(ctx, cmdSpec.Args[0], cmdSpec.Args[1:]...), nil
	}
	if cmdSpec.Shell != "" {
		return exec.CommandContext(ctx, "sh", "-c", cmdSpec.Shell), nil
	}
	return nil, fmt.Errorf("hook command is empty")
}

func buildHookEnv(env Envelope, h HookDef) []string {
	var vars []string
	add := func(key, value string) {
		if value == "" {
			return
		}
		vars = append(vars, key+"="+value)
	}

	add("CHORD_HOOK_POINT", env.Point)
	add("CHORD_HOOK_SESSION_ID", env.SessionID)
	if env.TurnID > 0 {
		add("CHORD_HOOK_TURN_ID", strconv.FormatUint(env.TurnID, 10))
	}
	add("CHORD_HOOK_AGENT_ID", env.AgentID)
	add("CHORD_HOOK_AGENT_KIND", env.AgentKind)
	add("CHORD_HOOK_PROJECT_ROOT", env.ProjectRoot)
	add("CHORD_HOOK_SELECTED_MODEL", env.SelectedModel)
	add("CHORD_HOOK_RUNNING_MODEL", env.RunningModel)

	data, _ := env.Data.(map[string]any)
	if toolName, _ := data["tool_name"].(string); toolName != "" {
		add("CHORD_HOOK_TOOL_NAME", toolName)
	}
	if timeoutMS, ok := intValue(data["timeout_ms"]); ok {
		add("CHORD_HOOK_TIMEOUT_MS", strconv.Itoa(timeoutMS))
	}
	if errorKind, _ := data["error_kind"].(string); errorKind != "" {
		add("CHORD_HOOK_ERROR_KIND", errorKind)
	}

	for key, value := range h.Environment {
		add(key, value)
	}
	return vars
}

func shouldRunHook(h HookDef, env Envelope) (bool, string) {
	h = normalizeHookDefaults(h)
	if len(h.Tools) > 0 && !matchesToolFilter(h.Tools, env) {
		return false, "tool_filter"
	}
	if len(h.Paths) > 0 && !matchesPathFilter(h.Paths, env) {
		return false, "path_filter"
	}
	if len(h.Agents) > 0 && !matchesStringPatterns(h.Agents, env.AgentID) {
		return false, "agent_filter"
	}
	if len(h.AgentKinds) > 0 && !containsString(h.AgentKinds, env.AgentKind) {
		return false, "agent_filter"
	}
	if len(h.Models) > 0 && !matchesModelFilter(h.Models, env) {
		return false, "model_filter"
	}
	if h.MinChangedFiles > 0 && changedFileCount(env) < h.MinChangedFiles {
		return false, "path_filter"
	}
	if h.OnlyOnError && !hasError(env) {
		return false, "only_on_error"
	}
	return true, ""
}

func matchesToolFilter(filters []string, env Envelope) bool {
	data, _ := env.Data.(map[string]any)
	if toolName, _ := data["tool_name"].(string); toolName != "" {
		return containsString(filters, toolName)
	}

	for _, tc := range toolCallsFromData(data) {
		if containsString(filters, tc.ToolName) {
			return true
		}
	}
	return false
}

func matchesPathFilter(filters []string, env Envelope) bool {
	data, _ := env.Data.(map[string]any)
	var candidates []string

	if path, _ := data["path"].(string); path != "" {
		candidates = append(candidates, normalizePath(path))
	}
	for _, path := range pathsFromData(data) {
		candidates = append(candidates, normalizePath(path))
	}
	for _, changed := range changedFilesFromData(data) {
		candidates = append(candidates, normalizePath(changed.Path))
	}

	for _, candidate := range candidates {
		for _, pattern := range filters {
			matched, err := doublestar.Match(pattern, candidate)
			if err == nil && matched {
				return true
			}
		}
	}
	return false
}

func matchesModelFilter(filters []string, env Envelope) bool {
	for _, filter := range filters {
		matchedSelected, _ := doublestar.Match(filter, env.SelectedModel)
		matchedRunning, _ := doublestar.Match(filter, env.RunningModel)
		if matchedSelected || matchedRunning {
			return true
		}
	}
	return false
}

func matchesStringPatterns(patterns []string, value string) bool {
	for _, pattern := range patterns {
		matched, err := doublestar.Match(pattern, value)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func changedFileCount(env Envelope) int {
	data, _ := env.Data.(map[string]any)
	return len(changedFilesFromData(data))
}

func hasError(env Envelope) bool {
	data, _ := env.Data.(map[string]any)
	if errText, _ := data["error"].(string); strings.TrimSpace(errText) != "" {
		return true
	}
	for _, tc := range toolCallsFromData(data) {
		if strings.TrimSpace(tc.Error) != "" {
			return true
		}
	}
	return false
}

type toolCallFilterItem struct {
	ToolName string
	Error    string
}

type changedFileFilterItem struct {
	Path string
}

func toolCallsFromData(data map[string]any) []toolCallFilterItem {
	raw, ok := data["tool_calls"].([]any)
	if !ok {
		return nil
	}

	items := make([]toolCallFilterItem, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["tool_name"].(string)
		errText, _ := m["error"].(string)
		items = append(items, toolCallFilterItem{ToolName: name, Error: errText})
	}
	return items
}

func changedFilesFromData(data map[string]any) []changedFileFilterItem {
	raw, ok := data["changed_files"].([]any)
	if !ok {
		return nil
	}

	items := make([]changedFileFilterItem, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		items = append(items, changedFileFilterItem{Path: path})
	}
	return items
}

func pathsFromData(data map[string]any) []string {
	switch raw := data["paths"].(type) {
	case []string:
		out := make([]string, 0, len(raw))
		for _, path := range raw {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			out = append(out, path)
		}
		return out
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			path, _ := item.(string)
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			out = append(out, path)
		}
		return out
	default:
		return nil
	}
}

func normalizePath(path string) string {
	return filepath.ToSlash(path)
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

func logHook(h HookDef, env Envelope, status string, action string, duration time.Duration, skippedReason string, err error) {
	attrs := []any{
		"point", env.Point,
		"hook_name", h.Name,
		"command_mode", h.Command.mode(),
		"duration_ms", duration.Milliseconds(),
		"action", action,
		"status", status,
		"skipped_reason", skippedReason,
		"agent_id", env.AgentID,
		"turn_id", env.TurnID,
	}
	if err != nil {
		attrs = append(attrs, "error", err)
	}

	switch status {
	case statusFailed, statusTimedOut:
		log.Warnf("hook execution attrs=%v", "<missing>")
	case statusSkipped:
		if hookDebugEnabled() {
			log.Debugf("hook execution attrs=%v", "<missing>")
		}
	default:
		log.Debugf("hook execution attrs=%v", "<missing>")
	}
}

func hookDebugEnabled() bool {
	return os.Getenv("CHORD_HOOK_DEBUG") == "1"
}
