package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func hookSessionID(sessionDir string) string {
	if sessionDir == "" {
		return ""
	}
	return filepath.Base(sessionDir)
}

func newHookEnvelope(
	point string,
	sessionDir string,
	turnID uint64,
	agentID string,
	agentKind string,
	projectRoot string,
	selectedModel string,
	runningModel string,
	data map[string]any,
) hook.Envelope {
	return hook.Envelope{
		Point:         point,
		Timestamp:     time.Now().UTC(),
		SessionID:     hookSessionID(sessionDir),
		TurnID:        turnID,
		AgentID:       agentID,
		AgentKind:     agentKind,
		ProjectRoot:   projectRoot,
		SelectedModel: selectedModel,
		RunningModel:  runningModel,
		Data:          data,
	}
}

func extractHookFilePath(args json.RawMessage) string {
	paths := extractHookFilePaths(args, "")
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func extractHookToolFilePaths(toolName string, args json.RawMessage, projectRoot string) []string {
	if tools.NormalizeName(toolName) == tools.NameApplyPatch {
		targets, err := tools.ApplyPatchTargets(args, projectRoot)
		if err == nil {
			return tools.MutationTargetPaths(targets)
		}
	}
	return extractHookFilePaths(args, projectRoot)
}

func extractHookFilePaths(args json.RawMessage, projectRoot string) []string {
	var parsed struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return nil
	}
	if parsed.Path != "" {
		return []string{parsed.Path}
	}
	if path := trackedEditPathFromArgs(args, projectRoot); path != "" {
		return []string{path}
	}
	return tools.NormalizeDeletePaths(parsed.Paths)
}

func buildToolHookData(tc message.ToolCall, projectRoot string) map[string]any {
	data := map[string]any{
		hook.DataKeyToolName: tc.Name,
		"args":               json.RawMessage(tc.Args),
	}
	if filePaths := extractHookToolFilePaths(tc.Name, tc.Args, projectRoot); len(filePaths) > 0 {
		data["paths"] = append([]string(nil), filePaths...)
		data["path"] = filePaths[0]
	}
	return data
}

func buildToolResultHookData(tcName string, argsJSON string, result string, err error, diff string, audit *message.ToolArgsAudit, fileState *message.ToolFileState) map[string]any {
	data := map[string]any{
		hook.DataKeyToolName: tcName,
		"result":             result,
		"diff":               diff,
	}
	if argsJSON != "" {
		data["args"] = json.RawMessage(argsJSON)
		if filePaths := toolResultFilePaths(tcName, json.RawMessage(argsJSON), fileState); len(filePaths) > 0 {
			data["paths"] = append([]string(nil), filePaths...)
			data["path"] = filePaths[0]
		}
	}
	if err != nil {
		data["error"] = err.Error()
	}
	if auditData := toolArgsAuditHookData(audit); auditData != nil {
		data["args_audit"] = auditData
	}
	return data
}

func toolArgsAuditHookData(audit *message.ToolArgsAudit) map[string]any {
	if audit == nil {
		return nil
	}
	data := map[string]any{
		"original_args_json":  audit.OriginalArgsJSON,
		"effective_args_json": audit.EffectiveArgsJSON,
		"user_modified":       audit.UserModified,
		"edit_summary":        audit.EditSummary,
	}
	if len(audit.IgnoredArgs) > 0 {
		data["ignored_args"] = append([]message.IgnoredToolArg(nil), audit.IgnoredArgs...)
	}
	if len(audit.InvalidArgs) > 0 {
		data["invalid_args"] = append([]message.InvalidToolArg(nil), audit.InvalidArgs...)
	}
	return data
}

func buildBeforeToolResultAppendData(tcName string, argsJSON string, rawResult string, displayResult string, contextResult string, err error, audit *message.ToolArgsAudit, fileState *message.ToolFileState) map[string]any {
	data := map[string]any{
		hook.DataKeyToolName: tcName,
		"raw_result":         rawResult,
		"display_result":     displayResult,
		"context_result":     contextResult,
	}
	if argsJSON != "" {
		data["args"] = json.RawMessage(argsJSON)
		if filePaths := toolResultFilePaths(tcName, json.RawMessage(argsJSON), fileState); len(filePaths) > 0 {
			data["paths"] = append([]string(nil), filePaths...)
			data["path"] = filePaths[0]
		}
	}
	if err != nil {
		data["error"] = err.Error()
	}
	if auditData := toolArgsAuditHookData(audit); auditData != nil {
		data["args_audit"] = auditData
	}
	return data
}

func applyBeforeToolResultAppendHook(currentDisplay string, currentContext string, result *hook.Result) (string, string) {
	if result == nil || result.Action != hook.ActionModify {
		return currentDisplay, currentContext
	}
	modified, ok := result.Data.(map[string]any)
	if !ok {
		return currentDisplay, currentContext
	}
	if v, ok := modified["display_result"].(string); ok {
		currentDisplay = v
	}
	if v, ok := modified["context_result"].(string); ok {
		currentContext = v
	}
	return currentDisplay, currentContext
}

func toolResultSummary(payload *ToolResultPayload, storedResult string, errText string) map[string]any {
	filePaths := toolResultFilePaths(payload.Name, json.RawMessage(payload.ArgsJSON), payload.FileState)
	summary := map[string]any{
		hook.DataKeyCallID:   payload.CallID,
		hook.DataKeyToolName: payload.Name,
		"args":               json.RawMessage(payload.ArgsJSON),
		"result":             storedResult,
		"diff":               payload.Diff,
		"error":              errText,
		"path":               firstString(filePaths),
		"paths":              filePaths,
		"is_changed":         payload.Diff != "" || payload.Name == tools.NameDelete,
		"is_deleted":         payload.Name == tools.NameDelete,
	}
	if payload.Audit != nil {
		summary["args_audit"] = toolArgsAuditHookData(payload.Audit)
	}
	return summary
}

func changedFileSummary(payload *ToolResultPayload) map[string]any {
	filePaths := toolResultFilePaths(payload.Name, json.RawMessage(payload.ArgsJSON), payload.FileState)
	if len(filePaths) == 0 {
		return nil
	}
	if payload.Name == tools.NameDelete {
		deleted := tools.ParseDeleteResult(payload.Result).Deleted
		if len(deleted) == 0 {
			return nil
		}
		return map[string]any{
			"paths":      append([]string(nil), deleted...),
			"path":       deleted[0],
			"tool":       payload.Name,
			"is_new":     false,
			"is_deleted": true,
			"diff":       "",
		}
	}
	if payload.Diff == "" {
		return nil
	}
	return map[string]any{
		"path":       filePaths[0],
		"paths":      append([]string(nil), filePaths...),
		"tool":       payload.Name,
		"is_new":     false,
		"is_deleted": false,
		"diff":       payload.Diff,
	}
}

func toolResultFilePaths(toolName string, args json.RawMessage, fileState *message.ToolFileState) []string {
	var paths []string
	if fileState != nil {
		for _, state := range fileState.Writes {
			if path := strings.TrimSpace(state.Path); path != "" {
				paths = append(paths, path)
			}
		}
		for _, state := range fileState.Deletes {
			if path := strings.TrimSpace(state.Path); path != "" {
				paths = append(paths, path)
			}
		}
	}
	if len(paths) > 0 {
		return paths
	}
	return extractHookToolFilePaths(toolName, args, "")
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func formatAutomationFeedback(h hook.HookDef, result hook.AutomationResult) string {
	body := selectAutomationBody(h, result)
	body = trimAutomationBody(body, h.MaxResultLines, h.MaxResultBytes)

	var sb strings.Builder
	fmt.Fprintf(&sb, "[hook:%s] Automated feedback from Chord hook\n", h.Name)
	fmt.Fprintf(&sb, "status: %s\n", result.Status)
	if result.Summary != "" {
		fmt.Fprintf(&sb, "summary: %s\n", result.Summary)
	}
	if body != "" {
		sb.WriteString("\n")
		sb.WriteString(body)
	}
	return strings.TrimSpace(sb.String())
}

func selectAutomationBody(h hook.HookDef, result hook.AutomationResult) string {
	format := h.ResultFormat
	if format == "" {
		format = hook.ResultFormatSummary
	}

	switch format {
	case hook.ResultFormatFull:
		if result.Body != "" {
			return result.Body
		}
		return result.Summary
	case hook.ResultFormatTail:
		if result.Body == "" {
			return result.Summary
		}
		lines := strings.Split(strings.TrimRight(result.Body, "\n"), "\n")
		maxLines := h.MaxResultLines
		if maxLines <= 0 {
			maxLines = hook.DefaultMaxResultLines
		}
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
		return strings.Join(lines, "\n")
	default:
		if result.Summary != "" {
			return result.Summary
		}
		return result.Body
	}
}

func trimAutomationBody(body string, maxLines int, maxBytes int) string {
	if maxLines <= 0 {
		maxLines = hook.DefaultMaxResultLines
	}
	if maxBytes <= 0 {
		maxBytes = hook.DefaultMaxResultBytes
	}

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		body = strings.Join(lines, "\n")
	}
	if len(body) > maxBytes {
		body = llm.TruncateStringBytes(body, maxBytes)
		body = strings.TrimRight(body, "\n") + "\n... (truncated)"
	}
	return strings.TrimSpace(body)
}

func shouldAppendAutomationResult(h hook.HookDef, result hook.AutomationResult) bool {
	if result.AppendContext {
		return true
	}
	switch h.Result {
	case hook.ResultAlwaysAppend:
		return true
	case hook.ResultAppendOnFailure:
		return result.Status == hook.AutomationStatusFailed
	default:
		return false
	}
}

func hookToastLevel(result hook.AutomationResult) string {
	switch strings.ToLower(result.Severity) {
	case "warn", "warning":
		return "warn"
	case "error":
		return "error"
	default:
		return "info"
	}
}

// Agent error categories reported through classifyAgentError. They extend the
// original agent/llm/tool taxonomy so a caller can act on the category instead
// of parsing the message text. Contract covers both a repairable result-contract
// violation and an unreadable result store: the error types differ (so the
// engine can tell them apart), but both mean the delivered result did not
// satisfy the task's declared contract. Blocked is the escalate kind of the same
// name — the worker declared the dead end itself.
const (
	agentErrorKindAgent    = "agent"
	agentErrorKindLLM      = "llm"
	agentErrorKindTool     = "tool"
	agentErrorKindContract = "contract"
	agentErrorKindBlocked  = tools.EscalateKindBlocked
)

func classifyAgentError(err error) string {
	if err == nil {
		return agentErrorKindAgent
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return agentErrorKindAgent
	}
	if _, ok := errors.AsType[*llm.APIError](err); ok {
		return agentErrorKindLLM
	}
	if _, ok := errors.AsType[*llm.AllKeysCoolingError](err); ok {
		return agentErrorKindLLM
	}
	if _, ok := errors.AsType[*llm.NoUsableKeysError](err); ok {
		return agentErrorKindLLM
	}
	if llm.IsContextLengthExceeded(err) {
		return agentErrorKindLLM
	}
	if _, ok := errors.AsType[*ResultContractViolationError](err); ok {
		return agentErrorKindContract
	}
	if _, ok := errors.AsType[*ResultContractIntegrityError](err); ok {
		return agentErrorKindContract
	}
	if _, ok := errors.AsType[*blockedEscalationError](err); ok {
		return agentErrorKindBlocked
	}
	msg := strings.ToLower(err.Error())
	switch {
	case classifyToolError(err) != "unknown", strings.Contains(msg, "tool execution failed"):
		return agentErrorKindTool
	case strings.Contains(msg, "llm"):
		return agentErrorKindLLM
	case strings.Contains(msg, "tool"):
		return agentErrorKindTool
	default:
		return agentErrorKindAgent
	}
}

func (a *MainAgent) fireHook(ctx context.Context, point string, turnID uint64, data map[string]any) (*hook.Result, error) {
	return a.hookEngine.Fire(ctx, newHookEnvelope(
		point,
		a.sessionDir,
		turnID,
		a.instanceID,
		"main",
		a.projectRoot,
		a.ProviderModelRef(),
		a.RunningModelRef(),
		data,
	))
}

// syncToolHooksConfigured reports whether user hooks exist at either sync tool
// point. When they do, speculative execution and its loop-side finalize hook
// step aside: calls dispatch through the execution pipeline, whose sync hooks
// run on the execution goroutine.
func (a *MainAgent) syncToolHooksConfigured() bool {
	return a.hookEngine.HasSyncHooks(hook.OnToolCall) ||
		a.hookEngine.HasSyncHooks(hook.OnBeforeToolResultAppend)
}

func (a *MainAgent) permRulesetIdentity() permRulesetIdentity {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return permRulesetIdentity{ruleset: rulesetSliceID(a.ruleset), length: len(a.ruleset), yolo: a.yoloEnabled.Load()}
}

// recordPermissionApproval caches a non-interactive allow decision taken on
// the event loop by the speculative-reuse prefilter.
func (a *MainAgent) recordPermissionApproval(turn *Turn, callID, name, args, cwd string) {
	if turn == nil {
		return
	}
	turn.recordPermissionApproval(callID, name, args, cwd, a.permRulesetIdentity())
}

// permissionApprovalMatches consults the current turn's cached allow decision
// from the execution pipeline's finalize path.
func (a *MainAgent) permissionApprovalMatches(callID, name, args, cwd string, pctx toolPermissionContext) bool {
	turn := a.currentTurn()
	if turn == nil {
		return false
	}
	return turn.permissionApprovalMatches(callID, name, args, cwd, a.permRulesetIdentity(), pctx)
}

func (a *MainAgent) fireHookBackground(ctx context.Context, point string, turnID uint64, data map[string]any) {
	a.hookEngine.FireBackground(ctx, newHookEnvelope(
		point,
		a.sessionDir,
		turnID,
		a.instanceID,
		"main",
		a.projectRoot,
		a.ProviderModelRef(),
		a.RunningModelRef(),
		data,
	))
}

func (a *MainAgent) runToolBatchHooks(ctx context.Context, turn *Turn) ([]hook.AutomationJobResult, error) {
	if turn == nil {
		return nil, nil
	}
	data := map[string]any{
		"tool_calls":    append([]any(nil), turn.CompletedToolCalls...),
		"changed_files": append([]any(nil), turn.ChangedFiles...),
	}
	return a.hookEngine.RunAutomation(ctx, newHookEnvelope(
		hook.OnToolBatchComplete,
		a.sessionDir,
		turn.ID,
		a.instanceID,
		"main",
		a.projectRoot,
		a.ProviderModelRef(),
		a.RunningModelRef(),
		data,
	))
}

func (a *MainAgent) appendHookFeedback(content string) {
	// Kind marks the feedback as synthetic: it is user-role (the model must
	// see it) but never user-authored, so it cannot hijack the latest-request
	// anchor of a context checkpoint or a terminal title.
	msg := message.Message{Role: "user", Content: content, Kind: message.KindHookFeedback}
	a.ctxMgr.Append(msg)
	if a.recoveryManager() != nil {
		a.persistAsync(identity.MainAgentID, msg)
	}
}

// syncToolHooksConfigured mirrors the parent gate: SubAgents share the
// parent's hook engine.
func (s *SubAgent) syncToolHooksConfigured() bool {
	if s == nil || s.parent == nil {
		return false
	}
	return s.parent.hookEngine.HasSyncHooks(hook.OnToolCall) ||
		s.parent.hookEngine.HasSyncHooks(hook.OnBeforeToolResultAppend)
}

func (s *SubAgent) permRulesetIdentity() permRulesetIdentity {
	if s == nil {
		return permRulesetIdentity{}
	}
	if published := s.rulesetPtr.Load(); published != nil {
		rs := *published
		return permRulesetIdentity{ruleset: rulesetSliceID(rs), length: len(rs)}
	}
	return permRulesetIdentity{}
}

// recordPermissionApproval caches a non-interactive allow decision taken on
// the SubAgent event loop by its speculative-reuse prefilter.
func (s *SubAgent) recordPermissionApproval(turn *Turn, callID, name, args, cwd string) {
	if turn == nil {
		return
	}
	turn.recordPermissionApproval(callID, name, args, cwd, s.permRulesetIdentity())
}

// permissionApprovalMatches consults the cached allow decision captured at
// dispatch time from the finalize path.
func (s *SubAgent) permissionApprovalMatches(turn *Turn, callID, name, args, cwd string, pctx toolPermissionContext) bool {
	if turn == nil {
		return false
	}
	return turn.permissionApprovalMatches(callID, name, args, cwd, s.permRulesetIdentity(), pctx)
}

func (s *SubAgent) fireHook(ctx context.Context, point string, turnID uint64, data map[string]any) (*hook.Result, error) {
	_, modelName := s.llmSnapshot()
	return s.parent.hookEngine.Fire(ctx, newHookEnvelope(
		point,
		s.sessionDir,
		turnID,
		s.instanceID,
		"sub",
		s.parent.projectRoot,
		modelName,
		modelName,
		data,
	))
}

func (s *SubAgent) fireHookBackground(ctx context.Context, point string, turnID uint64, data map[string]any) {
	_, modelName := s.llmSnapshot()
	s.parent.hookEngine.FireBackground(ctx, newHookEnvelope(
		point,
		s.sessionDir,
		turnID,
		s.instanceID,
		"sub",
		s.parent.projectRoot,
		modelName,
		modelName,
		data,
	))
}

func (s *SubAgent) runToolBatchHooks(ctx context.Context, turn *Turn) ([]hook.AutomationJobResult, error) {
	if turn == nil {
		return nil, nil
	}
	_, modelName := s.llmSnapshot()
	data := map[string]any{
		"tool_calls":    append([]any(nil), turn.CompletedToolCalls...),
		"changed_files": append([]any(nil), turn.ChangedFiles...),
	}
	return s.parent.hookEngine.RunAutomation(ctx, newHookEnvelope(
		hook.OnToolBatchComplete,
		s.sessionDir,
		turn.ID,
		s.instanceID,
		"sub",
		s.parent.projectRoot,
		modelName,
		modelName,
		data,
	))
}

func (s *SubAgent) appendHookFeedback(content string) {
	// Kind marks the feedback as synthetic (see MainAgent.appendHookFeedback).
	msg := message.Message{Role: "user", Content: content, Kind: message.KindHookFeedback}
	s.ctxMgr.Append(msg)
	s.persistMessageAsync(msg, "hook feedback", nil)
}
