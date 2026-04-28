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
	paths := extractHookFilePaths(args)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func extractHookFilePaths(args json.RawMessage) []string {
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
	return tools.NormalizeDeletePaths(parsed.Paths)
}

func buildToolHookData(tc message.ToolCall) map[string]any {
	data := map[string]any{
		"tool_name": tc.Name,
		"args":      json.RawMessage(tc.Args),
	}
	if filePaths := extractHookFilePaths(tc.Args); len(filePaths) > 0 {
		data["paths"] = append([]string(nil), filePaths...)
		data["path"] = filePaths[0]
	}
	return data
}

func buildToolResultHookData(tcName string, argsJSON string, result string, err error, diff string, audit *message.ToolArgsAudit) map[string]any {
	data := map[string]any{
		"tool_name": tcName,
		"result":    result,
		"diff":      diff,
	}
	if argsJSON != "" {
		data["args"] = json.RawMessage(argsJSON)
		if filePaths := extractHookFilePaths(json.RawMessage(argsJSON)); len(filePaths) > 0 {
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
	return map[string]any{
		"original_args_json":  audit.OriginalArgsJSON,
		"effective_args_json": audit.EffectiveArgsJSON,
		"user_modified":       audit.UserModified,
		"edit_summary":        audit.EditSummary,
	}
}

func buildBeforeToolResultAppendData(tcName string, argsJSON string, rawResult string, displayResult string, contextResult string, err error, audit *message.ToolArgsAudit) map[string]any {
	data := map[string]any{
		"tool_name":      tcName,
		"raw_result":     rawResult,
		"display_result": displayResult,
		"context_result": contextResult,
	}
	if argsJSON != "" {
		data["args"] = json.RawMessage(argsJSON)
		if filePaths := extractHookFilePaths(json.RawMessage(argsJSON)); len(filePaths) > 0 {
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
	summary := map[string]any{
		"call_id":    payload.CallID,
		"tool_name":  payload.Name,
		"args":       json.RawMessage(payload.ArgsJSON),
		"result":     storedResult,
		"diff":       payload.Diff,
		"error":      errText,
		"path":       extractHookFilePath(json.RawMessage(payload.ArgsJSON)),
		"paths":      extractHookFilePaths(json.RawMessage(payload.ArgsJSON)),
		"is_changed": payload.Diff != "" || payload.Name == "Delete",
		"is_deleted": payload.Name == "Delete",
	}
	if payload.Audit != nil {
		summary["args_audit"] = toolArgsAuditHookData(payload.Audit)
	}
	return summary
}

func changedFileSummary(payload *ToolResultPayload) map[string]any {
	filePaths := extractHookFilePaths(json.RawMessage(payload.ArgsJSON))
	if len(filePaths) == 0 {
		return nil
	}
	if payload.Name == "Delete" {
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
			maxLines = 50
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
		maxLines = 50
	}
	if maxBytes <= 0 {
		maxBytes = 4096
	}

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		body = strings.Join(lines, "\n")
	}
	if len(body) > maxBytes {
		body = body[:maxBytes]
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

func classifyAgentError(err error) string {
	if err == nil {
		return "agent"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "agent"
	}
	var apiErr *llm.APIError
	if errors.As(err, &apiErr) {
		return "llm"
	}
	var cooling *llm.AllKeysCoolingError
	if errors.As(err, &cooling) {
		return "llm"
	}
	var noUsable *llm.NoUsableKeysError
	if errors.As(err, &noUsable) {
		return "llm"
	}
	if llm.IsContextLengthExceeded(err) {
		return "llm"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case classifyToolError(err) != "unknown", strings.Contains(msg, "tool execution failed"):
		return "tool"
	case strings.Contains(msg, "llm"):
		return "llm"
	case strings.Contains(msg, "tool"):
		return "tool"
	default:
		return "agent"
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
	msg := message.Message{Role: "user", Content: content}
	a.ctxMgr.Append(msg)
	if a.recovery != nil {
		a.persistAsync("main", msg)
	}
}

func (s *SubAgent) fireHook(ctx context.Context, point string, turnID uint64, data map[string]any) (*hook.Result, error) {
	return s.parent.hookEngine.Fire(ctx, newHookEnvelope(
		point,
		s.sessionDir,
		turnID,
		s.instanceID,
		"sub",
		s.parent.projectRoot,
		s.modelName,
		s.modelName,
		data,
	))
}

func (s *SubAgent) fireHookBackground(ctx context.Context, point string, turnID uint64, data map[string]any) {
	s.parent.hookEngine.FireBackground(ctx, newHookEnvelope(
		point,
		s.sessionDir,
		turnID,
		s.instanceID,
		"sub",
		s.parent.projectRoot,
		s.modelName,
		s.modelName,
		data,
	))
}

func (s *SubAgent) runToolBatchHooks(ctx context.Context, turn *Turn) ([]hook.AutomationJobResult, error) {
	if turn == nil {
		return nil, nil
	}
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
		s.modelName,
		s.modelName,
		data,
	))
}

func (s *SubAgent) appendHookFeedback(content string) {
	msg := message.Message{Role: "user", Content: content}
	s.ctxMgr.Append(msg)
	go func() {
		if s.recovery != nil {
			_ = s.recovery.PersistMessage(s.instanceID, msg)
		}
	}()
}
