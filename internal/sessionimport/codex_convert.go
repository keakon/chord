package sessionimport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const (
	codexTopLevelTypeSessionMeta  = "session_meta"
	codexTopLevelTypeResponseItem = "response_item"
	codexTopLevelTypeTurnContext  = "turn_context"
	codexTopLevelTypeEventMsg     = "event_msg"

	codexTurnIDDefault        = "_default"
	codexTurnIDFallbackPrefix = "_fallback_"

	codexRoleDeveloper = "developer"
	codexRoleSystem    = "system"

	codexResponseItemTypeMessage              = "message"
	codexResponseItemTypeFunctionCall         = "function_call"
	codexResponseItemTypeFunctionCallOutput   = "function_call_output"
	codexResponseItemTypeCustomToolCall       = "custom_tool_call"
	codexResponseItemTypeCustomToolCallOutput = "custom_tool_call_output"
	codexResponseItemTypeReasoning            = "reasoning"

	// codexExecToolName is Codex's freeform code-mode tool. It is the only
	// top-level tool the current CLI exposes: the real work happens in nested
	// `tools.<name>(...)` calls inside the JavaScript input, so the name alone
	// says nothing about which Chord tool ran.
	codexExecToolName = "exec"
	// codexCollaborationNamespace marks Codex's multi-agent tools
	// (spawn_agent, wait_agent, send_message, list_agents).
	codexCollaborationNamespace = "collaboration"
)

var errEmptyInput = fmt.Errorf("codex import: empty input")

// ---------------------------------------------------------------------------
// Parse JSONL → Codex IR
// ---------------------------------------------------------------------------

// parseCodexRollout parses raw JSONL bytes into a list of typed rollout
// entries that can be used for IR construction.
type codexRolloutEntry struct {
	Timestamp string
	EventType string // top-level "type" field: session_meta, response_item, event_msg, turn_context, compacted
	Payload   json.RawMessage
}

// parseCodexJSONL scans JSONL and returns typed entries.
func parseCodexJSONL(data []byte) ([]codexRolloutEntry, string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	var entries []codexRolloutEntry
	var sessionID string
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var lineObj map[string]json.RawMessage
		if err := importJSONUnmarshalString(line, &lineObj); err != nil {
			return nil, "", fmt.Errorf("codex import: line %d: parse JSON: %w", lineNo, err)
		}

		rawType, ok := lineObj["type"]
		if !ok {
			continue
		}
		var typeStr string
		if err := importJSONUnmarshal(rawType, &typeStr); err != nil || typeStr == "" {
			continue
		}
		payload := lineObj["payload"]
		entries = append(entries, codexRolloutEntry{
			Timestamp: pickJSONString(lineObj, "timestamp"),
			EventType: typeStr,
			Payload:   payload,
		})
		if sessionID == "" && typeStr == codexTopLevelTypeSessionMeta && len(payload) > 0 {
			var meta struct {
				ID string `json:"id"`
			}
			if importJSONUnmarshal(payload, &meta) == nil && meta.ID != "" {
				sessionID = meta.ID
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, "", fmt.Errorf("codex import: scan JSONL: %w", err)
	}
	return entries, sessionID, nil
}

// pickJSONString extracts a string value from a map of raw JSON messages.
func pickJSONString(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if err := importJSONUnmarshal(raw, &s); err == nil {
			s = strings.TrimSpace(s)
			if s != "" {
				return s
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Build IR from parsed entries
// ---------------------------------------------------------------------------

// buildCodexIR transforms parsed rollout entries into an intermediate
// representation with reconstructed turns and structured tool lineage.
//
// Source precedence:
//   - response_item is the canonical content source
//   - event_msg supplements metadata (turn boundaries, usage, status)
//   - turn_context defines turn identity
func buildCodexIR(entries []codexRolloutEntry, reasoningMode string, report *ImportReport) ([]*codexTurn, error) {
	// Track current turn_id from turn_context / turn_started events.
	var currentTurnID string
	turnOrder := make(map[string]int) // turn_id -> order
	var turnOrderList []string
	fallbackTurnSeq := 0

	// Pre-scan to identify turn boundaries.
	for _, e := range entries {
		switch e.EventType {
		case codexTopLevelTypeTurnContext:
			var tc struct {
				TurnID string `json:"turn_id"`
			}
			if err := json.Unmarshal(e.Payload, &tc); err == nil && tc.TurnID != "" {
				if _, exists := turnOrder[tc.TurnID]; !exists {
					turnOrder[tc.TurnID] = len(turnOrderList)
					turnOrderList = append(turnOrderList, tc.TurnID)
				}
			}
		case codexTopLevelTypeEventMsg:
			var ev struct {
				Type   string `json:"type"`
				TurnID string `json:"turn_id"`
			}
			if err := json.Unmarshal(e.Payload, &ev); err == nil {
				if ev.Type == "task_started" || ev.Type == "turn_started" {
					if ev.TurnID != "" {
						if _, exists := turnOrder[ev.TurnID]; !exists {
							turnOrder[ev.TurnID] = len(turnOrderList)
							turnOrderList = append(turnOrderList, ev.TurnID)
						}
					}
				}
			}
		}
	}

	turns := make(map[string]*codexTurn)
	callOwners := make(map[string]string) // call_id -> owning turn_id
	newFallbackTurnID := func() string {
		fallbackTurnSeq++
		return fmt.Sprintf("_fallback_%d", fallbackTurnSeq)
	}
	getOrCreateTurn := func(turnID string) *codexTurn {
		if turnID == "" {
			turnID = codexTurnIDDefault
		}
		if t, ok := turns[turnID]; ok {
			return t
		}
		t := &codexTurn{
			TurnID:      turnID,
			ToolCalls:   make(map[string]*codexToolCall),
			ToolResults: make(map[string]*codexToolResult),
		}
		if turnID != codexTurnIDDefault && strings.HasPrefix(turnID, codexTurnIDFallbackPrefix) {
			t.FallbackTurnNumber = fallbackTurnSeq
		}
		turns[turnID] = t
		if _, exists := turnOrder[turnID]; !exists {
			turnOrder[turnID] = len(turnOrderList)
			turnOrderList = append(turnOrderList, turnID)
		}
		return t
	}

	sourceOrder := 0
	responseItemRoles := make(map[string]map[message.Role]bool)
	recordResponseItemRole := func(turnID string, role message.Role) {
		if _, ok := responseItemRoles[turnID]; !ok {
			responseItemRoles[turnID] = make(map[message.Role]bool)
		}
		responseItemRoles[turnID][role] = true
	}
	hasResponseItemRole := func(turnID string, role message.Role) bool {
		if m, ok := responseItemRoles[turnID]; ok {
			return m[role]
		}
		return false
	}
	resolveActiveTurnID := func(role message.Role) string {
		if currentTurnID != "" {
			return currentTurnID
		}
		if role == message.RoleUser {
			if t, ok := turns[codexTurnIDDefault]; ok {
				hasAssistantActivity := len(t.AssistantMessages) > 0 || len(t.ToolCalls) > 0 || len(t.ToolResults) > 0
				if hasAssistantActivity {
					newID := newFallbackTurnID()
					currentTurnID = newID
					return newID
				}
			}
		}
		return codexTurnIDDefault
	}

	for _, e := range entries {
		switch e.EventType {
		case codexTopLevelTypeResponseItem:
			if err := codexProcessResponseItem(e.Payload, resolveActiveTurnID, reasoningMode, getOrCreateTurn, recordResponseItemRole, callOwners, &sourceOrder, report); err != nil {
				report.warnf("response_item parse error: %v", err)
			}

		case codexTopLevelTypeTurnContext:
			var tc struct {
				TurnID string `json:"turn_id"`
			}
			if err := json.Unmarshal(e.Payload, &tc); err == nil && tc.TurnID != "" {
				currentTurnID = tc.TurnID
				turn := getOrCreateTurn(currentTurnID)
				turn.HasTurnContext = true
				turn.HasExplicitTurnID = true
			}

		case codexTopLevelTypeEventMsg:
			codexProcessEventMsg(e.Payload, resolveActiveTurnID, getOrCreateTurn, hasResponseItemRole, &sourceOrder, report)
		}
	}

	for _, t := range turns {
		t.UserMessages = codexResolveMessageConflicts(t.UserMessages, report)
		t.AssistantMessages = codexResolveMessageConflicts(t.AssistantMessages, report)
	}

	if len(turnOrderList) == 0 {
		turnOrderList = append(turnOrderList, "_default")
	}
	result := make([]*codexTurn, 0, len(turnOrderList))
	for _, tid := range turnOrderList {
		if t, ok := turns[tid]; ok {
			result = append(result, t)
		}
	}

	return result, nil
}

// codexProcessResponseItem handles a single response_item payload.
func codexProcessResponseItem(
	payload json.RawMessage,
	resolveActiveTurnID func(message.Role) string,
	reasoningMode string,
	getOrCreateTurn func(string) *codexTurn,
	recordResponseItemRole func(string, message.Role),
	callOwners map[string]string,
	sourceOrder *int,
	report *ImportReport,
) error {
	var item struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		TurnID    string          `json:"turn_id"`
		TurnID2   string          `json:"turnId"`
		Content   json.RawMessage `json:"content"`
		Name      string          `json:"name"`
		Namespace string          `json:"namespace"`
		CallID    string          `json:"call_id"`
		Args      string          `json:"arguments"`
		Input     json.RawMessage `json:"input"`
		Output    json.RawMessage `json:"output"`
		Status    string          `json:"status"`
	}
	if err := json.Unmarshal(payload, &item); err != nil {
		return err
	}

	turnID := strings.TrimSpace(item.TurnID)
	if turnID == "" {
		turnID = strings.TrimSpace(item.TurnID2)
	}
	if turnID == "" {
		if owner := strings.TrimSpace(callOwners[item.CallID]); owner != "" {
			turnID = owner
		}
	}
	if turnID == "" {
		turnID = resolveActiveTurnID(codexMessageRole(item.Role))
	}
	turn := getOrCreateTurn(turnID)
	if strings.TrimSpace(item.TurnID) != "" || strings.TrimSpace(item.TurnID2) != "" {
		turn.HasExplicitTurnID = true
	}
	*sourceOrder++

	switch item.Type {
	case codexResponseItemTypeMessage:
		role := strings.ToLower(item.Role)
		if role == codexRoleDeveloper || role == codexRoleSystem {
			report.SkippedEntries++
			return nil
		}
		text := codexExtractContentText(item.Content)
		msgRole := codexMessageRole(role)
		if msgRole == "" {
			msgRole = message.RoleAssistant
		}
		if msgRole == message.RoleUser && isCodexStartupInstructionMessage(text) {
			report.SkippedEntries++
			return nil
		}
		if strings.TrimSpace(text) == "" {
			report.SkippedEntries++
			return nil
		}
		recordResponseItemRole(turnID, msgRole)
		if msgRole == message.RoleUser {
			turn.UserMessages = append(turn.UserMessages, codexMessageItem{
				Role:        message.RoleUser,
				Content:     text,
				Source:      codexMessageSourceResponseItem,
				SourceOrder: *sourceOrder,
			})
		} else {
			turn.AssistantMessages = append(turn.AssistantMessages, codexMessageItem{
				Role:        message.RoleAssistant,
				Content:     text,
				Source:      codexMessageSourceResponseItem,
				SourceOrder: *sourceOrder,
			})
		}

	case codexResponseItemTypeFunctionCall:
		callID := item.CallID
		if callID == "" {
			report.MissingToolCallIDs++
			report.SkippedEntries++
			report.warnf("function_call without call_id, skipped")
			return nil
		}
		turn.ToolCalls[callID] = &codexToolCall{
			CallID:      callID,
			Name:        item.Name,
			Namespace:   strings.TrimSpace(item.Namespace),
			Arguments:   json.RawMessage(item.Args),
			TurnID:      turnID,
			SourceOrder: *sourceOrder,
		}
		callOwners[callID] = turnID

	case codexResponseItemTypeFunctionCallOutput:
		callID := item.CallID
		if callID == "" {
			report.MissingToolCallIDs++
			report.SkippedEntries++
			report.warnf("function_call_output without call_id, skipped")
			return nil
		}
		if owner := strings.TrimSpace(callOwners[callID]); owner != "" && owner != turnID {
			turnID = owner
			turn = getOrCreateTurn(turnID)
		}
		outputText := codexExtractOutputText(item.Output)
		turn.ToolResults[callID] = &codexToolResult{
			CallID:      callID,
			Output:      outputText,
			TurnID:      turnID,
			SourceOrder: *sourceOrder,
			Status:      strings.TrimSpace(item.Status),
		}

	case codexResponseItemTypeCustomToolCall:
		callID := item.CallID
		if callID == "" {
			report.SkippedEntries++
			return nil
		}
		inputStr := codexRawString(item.Input)
		call := &codexToolCall{
			CallID:      callID,
			Name:        item.Name,
			TurnID:      turnID,
			SourceOrder: *sourceOrder,
		}
		if codexIsExecToolName(item.Name) {
			// Codex's code-mode `exec` input is raw JavaScript, so it needs
			// analysis rather than argument parsing. Keep the source verbatim
			// for an unmapped call's record and parse it for the mapping.
			call.ExecInput = inputStr
			if script, ok := parseCodexExecScript(inputStr); ok {
				call.ExecScript = &script
				report.CodexExecScripts++
			}
		} else {
			// Legacy freeform custom tools (apply_patch) carry their input as
			// the tool's own string parameter.
			argsJSON, _ := json.Marshal(map[string]string{"input": inputStr})
			call.Arguments = argsJSON
		}
		turn.ToolCalls[callID] = call
		callOwners[callID] = turnID

	case codexResponseItemTypeCustomToolCallOutput:
		callID := item.CallID
		if callID == "" {
			report.SkippedEntries++
			return nil
		}
		if owner := strings.TrimSpace(callOwners[callID]); owner != "" && owner != turnID {
			turnID = owner
			turn = getOrCreateTurn(turnID)
		}
		outputText := codexExtractCustomToolOutputText(item.Output)
		turn.ToolResults[callID] = &codexToolResult{
			CallID:      callID,
			Output:      outputText,
			TurnID:      turnID,
			SourceOrder: *sourceOrder,
			Status:      strings.TrimSpace(item.Status),
		}

	case codexResponseItemTypeReasoning:
		reasoningText := codexExtractReasoningText(payload)
		if strings.TrimSpace(reasoningText) == "" {
			report.ReasoningBlocksSkipped++
			report.SkippedEntries++
			return nil
		}
		switch reasoningMode {
		case ReasoningVisible:
			turn.ReasoningEntries = append(turn.ReasoningEntries, codexReasoningEntry{
				Text:        reasoningText,
				TurnID:      turnID,
				SourceOrder: *sourceOrder,
			})
		default:
			report.ReasoningBlocksSkipped++
			report.SkippedEntries++
		}

	case "web_search_call", "image_generation_call", "local_shell_call", "tool_search_call", "tool_search_output", "compaction", "context_compaction", "other":
		report.SkippedEntries++
	default:
		report.SkippedEntries++
	}

	return nil
}

// codexProcessEventMsg handles a single event_msg payload.
func codexProcessEventMsg(
	payload json.RawMessage,
	resolveActiveTurnID func(message.Role) string,
	getOrCreateTurn func(string) *codexTurn,
	hasResponseItemRole func(string, message.Role) bool,
	sourceOrder *int,
	report *ImportReport,
) {
	var ev struct {
		Type    string          `json:"type"`
		TurnID  string          `json:"turn_id"`
		Message string          `json:"message"`
		Text    string          `json:"text"`
		CallID  string          `json:"call_id"`
		Query   string          `json:"query"`
		Action  json.RawMessage `json:"action"`
		// TokenCountEvent fields
		Info *struct {
			TotalTokenUsage *struct {
				InputTokens           int `json:"input_tokens"`
				CachedInputTokens     int `json:"cached_input_tokens"`
				OutputTokens          int `json:"output_tokens"`
				ReasoningOutputTokens int `json:"reasoning_output_tokens"`
			} `json:"total_token_usage"`
			LastTokenUsage *struct {
				InputTokens           int `json:"input_tokens"`
				CachedInputTokens     int `json:"cached_input_tokens"`
				OutputTokens          int `json:"output_tokens"`
				ReasoningOutputTokens int `json:"reasoning_output_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return
	}

	turnID := resolveActiveTurnID("")
	if ev.TurnID != "" {
		turnID = ev.TurnID
	}

	switch ev.Type {
	case "agent_message":
		// Fallback: only use event_msg if no response_item message for this turn.
		if hasResponseItemRole(turnID, message.RoleAssistant) {
			report.SkippedDuplicates++
			report.SkippedEntries++
			return
		}
		text := strings.TrimSpace(ev.Message)
		if text == "" {
			text = strings.TrimSpace(ev.Text)
		}
		if text == "" {
			report.SkippedEntries++
			return
		}
		turn := getOrCreateTurn(turnID)
		*sourceOrder++
		turn.AssistantMessages = append(turn.AssistantMessages, codexMessageItem{
			Role:        message.RoleAssistant,
			Content:     text,
			Source:      codexMessageSourceEventMsg,
			SourceOrder: *sourceOrder,
		})

	case "user_message":
		if hasResponseItemRole(turnID, message.RoleUser) {
			report.SkippedDuplicates++
			report.SkippedEntries++
			return
		}
		text := strings.TrimSpace(ev.Message)
		if text == "" {
			text = strings.TrimSpace(ev.Text)
		}
		if text == "" {
			report.SkippedEntries++
			return
		}
		turn := getOrCreateTurn(turnID)
		*sourceOrder++
		turn.UserMessages = append(turn.UserMessages, codexMessageItem{
			Role:        message.RoleUser,
			Content:     text,
			Source:      codexMessageSourceEventMsg,
			SourceOrder: *sourceOrder,
		})

	case "token_count":
		// Extract token usage for potential attachment.
		if ev.Info != nil && ev.Info.LastTokenUsage != nil {
			turn := getOrCreateTurn(turnID)
			usage := ev.Info.LastTokenUsage
			turn.UsageEvents = append(turn.UsageEvents, codexTokenUsageEvent{
				InputTokens:  usage.InputTokens,
				OutputTokens: usage.OutputTokens,
				CacheTokens:  usage.CachedInputTokens,
				ReasonTokens: usage.ReasoningOutputTokens,
				TurnID:       turnID,
			})
		}
		report.SkippedEntries++

	case "agent_reasoning":
		// event_msg reasoning is supplemental; we track it but the
		// primary source is response_item.reasoning.
		report.ReasoningBlocksSkipped++
		report.SkippedEntries++

	case "web_search_end":
		callID := strings.TrimSpace(ev.CallID)
		if callID == "" {
			report.MissingToolCallIDs++
			report.SkippedEntries++
			report.warnf("web_search_end without call_id, skipped")
			return
		}
		turn := getOrCreateTurn(turnID)
		*sourceOrder++
		turn.ToolCalls[callID] = &codexToolCall{
			CallID:      callID,
			Name:        "web_search",
			Arguments:   codexWebSearchArgs(ev.Query, ev.Action),
			TurnID:      turnID,
			SourceOrder: *sourceOrder,
		}

	case "task_started", "turn_started", "task_complete", "turn_complete",
		"patch_apply_end", "patch_apply_begin", "web_search_begin",
		"exec_command_begin", "exec_command_end", "exec_command_output_delta",
		"mcp_tool_call_begin", "mcp_tool_call_end", "mcp_startup_update", "mcp_startup_complete",
		"image_generation_begin", "image_generation_end",
		"context_compacted", "error", "warning", "turn_aborted", "turn_diff",
		"model_reroute", "model_verification", "stream_error", "deprecation_notice":
		// Supplemental/status events – skip silently.
		report.SkippedStatusEvents++
		report.SkippedEntries++

	default:
		report.SkippedEntries++
	}
}

func isCodexStartupInstructionMessage(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "# AGENTS.md instructions for ") || strings.HasPrefix(trimmed, "# instructions")
}

func codexMessageRole(raw string) message.Role {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(message.RoleUser):
		return message.RoleUser
	case string(message.RoleAssistant):
		return message.RoleAssistant
	default:
		return ""
	}
}

func codexWebSearchArgs(query string, action json.RawMessage) json.RawMessage {
	args := map[string]any{}
	if strings.TrimSpace(query) != "" {
		args["query"] = strings.TrimSpace(query)
	}
	if len(bytes.TrimSpace(action)) > 0 && !bytes.Equal(bytes.TrimSpace(action), []byte("null")) {
		var v any
		if json.Unmarshal(action, &v) == nil {
			args["action"] = v
		} else {
			args["action"] = string(action)
		}
	}
	b, _ := json.Marshal(args)
	return b
}

func codexResolveMessageConflicts(items []codexMessageItem, report *ImportReport) []codexMessageItem {
	if len(items) <= 1 {
		return items
	}
	var responseItems []codexMessageItem
	var eventItems []codexMessageItem
	for _, item := range items {
		if item.Source == codexMessageSourceResponseItem {
			responseItems = append(responseItems, item)
		} else {
			eventItems = append(eventItems, item)
		}
	}
	if len(responseItems) == 0 || len(eventItems) == 0 {
		return items
	}
	canonical := responseItems[0]
	for _, ev := range eventItems {
		if strings.TrimSpace(ev.Content) == strings.TrimSpace(canonical.Content) {
			report.SkippedDuplicates++
		} else {
			report.DuplicateSourceConflicts++
		}
		report.SkippedEntries++
	}
	return responseItems
}

func codexAttachReasoning(turn *codexTurn, report *ImportReport) (string, bool) {
	if len(turn.ReasoningEntries) == 0 {
		return "", false
	}
	if len(turn.AssistantMessages)+len(turn.ToolCalls) > 1 {
		report.SkippedAmbiguousReasoning += len(turn.ReasoningEntries)
		return "", false
	}
	var texts []string
	for _, entry := range turn.ReasoningEntries {
		if strings.TrimSpace(entry.Text) != "" {
			texts = append(texts, strings.TrimSpace(entry.Text))
		}
	}
	if len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, "\n\n"), true
}

func codexAttachUsage(turn *codexTurn) (*message.TokenUsage, bool) {
	if len(turn.UsageEvents) == 0 {
		return nil, false
	}
	if len(turn.AssistantMessages)+len(turn.ToolCalls) != 1 {
		return nil, false
	}
	usage := &message.TokenUsage{}
	for _, ev := range turn.UsageEvents {
		usage.InputTokens += ev.InputTokens
		usage.OutputTokens += ev.OutputTokens
		usage.CacheReadTokens += ev.CacheTokens
		usage.ReasoningTokens += ev.ReasonTokens
	}
	return usage, true
}

// codexExtractContentText extracts text from a response_item message's
// content array, supporting both input_text and output_text content types.
func codexExtractContentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		// Try as a plain string.
		var s string
		if err := json.Unmarshal(content, &s); err == nil {
			return strings.TrimSpace(s)
		}
		return ""
	}
	var out []string
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text":
			if t := strings.TrimSpace(p.Text); t != "" {
				out = append(out, t)
			}
		}
	}
	return strings.Join(out, "\n\n")
}

// codexRawString reads a JSON string field, tolerating an absent, null, or
// non-string value.
func codexRawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// codexIsExecToolName reports whether a Codex tool name is the code-mode `exec`
// tool.
func codexIsExecToolName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), codexExecToolName)
}

// codexExtractCustomToolOutputText extracts text from a
// custom_tool_call_output's output field.
//
// Codex builds these by appending: the first content item is the result header
// ("Script completed\nWall time …\nOutput:\n", which already ends in a newline)
// and the rest are the script's own output, so the items concatenate. Joining
// them as separate blocks would insert a blank line into every result body.
func codexExtractCustomToolOutputText(output json.RawMessage) string {
	if len(output) == 0 {
		return ""
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(output, &parts); err != nil {
		return codexExtractOutputText(output)
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// codexExtractOutputText extracts text from a function_call_output's output field.
// The output can be either a plain string or an array of content items.
func codexExtractOutputText(output json.RawMessage) string {
	if len(output) == 0 {
		return ""
	}
	// Try as string first.
	var s string
	if err := json.Unmarshal(output, &s); err == nil {
		return s
	}
	// Try as content items array.
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(output, &parts); err == nil {
		var texts []string
		for _, p := range parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return string(output)
}

// codexExtractReasoningText extracts visible reasoning text from a
// response_item.reasoning payload.
func codexExtractReasoningText(payload json.RawMessage) string {
	var item struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Summary []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(payload, &item); err != nil {
		return ""
	}
	// Prefer content over summary.
	for _, c := range item.Content {
		if c.Text != "" {
			return c.Text
		}
	}
	for _, s := range item.Summary {
		if s.Text != "" {
			return s.Text
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Stage 3: Linearize IR → Chord messages
// ---------------------------------------------------------------------------

// linearizeCodexTurns converts reconstructed turns into a linear sequence
// of Chord messages suitable for transcript restore and context replay.
//
// Output ordering per turn:
// 1. Canonical user message(s)
// 2. Assistant tool-call message(s)
// 3. Corresponding tool-result message(s)
// 4. Assistant plain-text response(s) (after tool execution)
func linearizeCodexTurns(turns []*codexTurn, report *ImportReport) []message.Message {
	var out []message.Message

	for _, turn := range turns {
		reasoningText, reasoningAttached := codexAttachReasoning(turn, report)
		usage, usageAttached := codexAttachUsage(turn)
		if usageAttached {
			report.UsageEventsAttached += len(turn.UsageEvents)
		} else if len(turn.UsageEvents) > 0 {
			report.UsageEventsSkipped += len(turn.UsageEvents)
		}

		type orderedItem struct {
			kind   codexOrderedKind
			order  int
			user   codexMessageItem
			assist codexMessageItem
			callID string
		}
		var ordered []orderedItem
		for _, um := range turn.UserMessages {
			ordered = append(ordered, orderedItem{kind: codexOrderedKindUser, order: um.SourceOrder, user: um})
		}
		for _, am := range turn.AssistantMessages {
			ordered = append(ordered, orderedItem{kind: codexOrderedKindAssistant, order: am.SourceOrder, assist: am})
		}
		for callID, tc := range turn.ToolCalls {
			ordered = append(ordered, orderedItem{kind: codexOrderedKindToolCall, order: tc.SourceOrder, callID: callID})
		}
		for i := 0; i < len(ordered); i++ {
			for j := i + 1; j < len(ordered); j++ {
				if ordered[j].order < ordered[i].order {
					ordered[i], ordered[j] = ordered[j], ordered[i]
				}
			}
		}

		for _, item := range ordered {
			switch item.kind {
			case codexOrderedKindUser:
				out = append(out, message.Message{
					Role:       message.RoleUser,
					Content:    item.user.Content,
					Provenance: importedCodexProvenance(),
				})
			case codexOrderedKindAssistant:
				msg := message.Message{
					Role:       message.RoleAssistant,
					Content:    item.assist.Content,
					Provenance: importedCodexProvenance(),
				}
				if reasoningText != "" {
					msg.ReasoningContent = reasoningText
					reasoningText = ""
					reasoningAttached = false
				}
				if usage != nil {
					msg.Usage = usage
					usage = nil
				}
				out = append(out, msg)
			case codexOrderedKindToolCall:
				tc := turn.ToolCalls[item.callID]
				tr := turn.ToolResults[item.callID]

				chordName, normArgs, mapped := codexResolveChordTool(tc)
				keep := mapped
				if tr != nil {
					if res, ok := parseCodexExecResult(tr.Output); ok && res.yielded() {
						// A yielded exec has no outcome: its cell keeps running in
						// the source session and a later `wait` call holds the
						// continuation. Mapping the yielded record to a finished
						// Chord tool would invent a result that never happened, so
						// the whole record stays verbatim, cell id included.
						keep = false
					}
				}
				if !keep {
					// No Chord tool performs this work, but the trajectory is
					// still real history: keep the call under the name and
					// arguments the source agent used, and its result, exactly
					// as they were issued. Rewriting the call into text would
					// drop an action that ran, and folding the arguments into
					// importer metadata would show bookkeeping instead of the
					// original call. The model reads the preserved form as the
					// action it was — completed or still running.
					chordName, normArgs = codexImportedToolCall(tc)
					report.KeptSourceToolCalls++
				} else {
					report.StructuredToolCalls++
				}

				out = append(out, message.Message{
					Role: message.RoleAssistant,
					ToolCalls: []message.ToolCall{{
						ID:   tc.CallID,
						Name: chordName,
						Args: normArgs,
					}},
					ReasoningContent: reasoningText,
					Usage:            usage,
					Provenance:       importedCodexProvenance(),
				})
				if reasoningAttached {
					reasoningText = ""
					reasoningAttached = false
				}
				if usage != nil {
					usage = nil
				}

				if tr != nil {
					status := message.ToolStatusSuccess
					if tr.Status != "" {
						status = tr.Status
					}
					content := tr.Output
					// Codex prefixes every result with "Script completed\nWall
					// time N seconds\nOutput:\n". Strip it so an imported shell
					// result is the command's output, like a live one, and let a
					// failed or terminated script surface as an error result. A
					// yielded script is excluded from the mapping above, so its
					// envelope stays verbatim here.
					if res, ok := parseCodexExecResult(content); ok && !res.yielded() {
						content = res.Output
						if !res.succeeded() {
							status = message.ToolStatusError
						}
					} else if codexHasShellEnvelope(tc.Name) {
						// The function-call shell tools carry the same kind of
						// header, with the exit status on its own line. A live
						// Chord shell reports a non-zero exit as a failed
						// result, so an imported one has to as well; otherwise a
						// failed build reads back as a success and the model
						// trusts output the command never produced. A result
						// that is still running keeps its header: the session id
						// in it names the continuation the source recorded, and
						// the call stays mapped to `shell` because running the
						// command is what that tool does — the rest of the
						// output belongs to a later `write_stdin` record, not to
						// this result.
						if res, ok := parseCodexShellResult(content); ok && res.Settled {
							content = res.Output
							if res.ExitCode != 0 {
								status = message.ToolStatusError
							}
						}
					}
					// A payload-wrapped result keeps its exit status inside the
					// payload, so read it there too; the text it wraps is what
					// the command printed or the patch reported.
					if codexRecordsResultPayload(tc.Name) {
						if payload, ok := parseCodexResultPayload(content); ok {
							content = payload.Output
							if payload.ExitCode != 0 {
								status = message.ToolStatusError
							}
						}
					}
					out = append(out, message.Message{
						Role:       message.RoleTool,
						ToolCallID: tr.CallID,
						Content:    content,
						ToolStatus: status,
						Provenance: importedCodexProvenance(),
					})
					report.StructuredToolResults++
					delete(turn.ToolResults, item.callID)
				}
			}
		}

		type orphanResult struct {
			callID string
			order  int
		}
		var orphaned []orphanResult
		for callID, tr := range turn.ToolResults {
			orphaned = append(orphaned, orphanResult{callID: callID, order: tr.SourceOrder})
		}
		for i := 0; i < len(orphaned); i++ {
			for j := i + 1; j < len(orphaned); j++ {
				if orphaned[j].order < orphaned[i].order {
					orphaned[i], orphaned[j] = orphaned[j], orphaned[i]
				}
			}
		}
		for _, orphan := range orphaned {
			out = append(out, codexOrphanedToolResultFallback(turn.ToolResults[orphan.callID]))
			report.MissingToolDeclarations++
		}
	}

	filtered := out[:0]
	for _, m := range out {
		if m.Role == message.RoleTool || len(m.ToolCalls) > 0 {
			filtered = append(filtered, m)
			continue
		}
		if strings.TrimSpace(m.Content) != "" || len(m.Parts) > 0 {
			filtered = append(filtered, m)
		}
	}
	report.ToolEntriesRendered = report.StructuredToolCalls + report.StructuredToolResults +
		report.KeptSourceToolCalls + report.MissingToolDeclarations

	return filtered
}

func validateImportedCodexMessages(msgs []message.Message, report *ImportReport) error {
	declared := make(map[string]struct{})
	for i, msg := range msgs {
		switch msg.Role {
		case message.RoleAssistant:
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" || tc.Name == "" || len(tc.Args) == 0 {
					report.ValidationFailures++
					return fmt.Errorf("codex import: invalid structured tool call at message %d", i)
				}
				declared[tc.ID] = struct{}{}
			}
		case message.RoleTool:
			if msg.ToolCallID == "" {
				report.ValidationFailures++
				return fmt.Errorf("codex import: tool result missing tool_call_id at message %d", i)
			}
			if _, ok := declared[msg.ToolCallID]; !ok {
				report.ValidationFailures++
				return fmt.Errorf("codex import: tool result references undeclared tool_call_id %q", msg.ToolCallID)
			}
		case message.RoleUser:
		default:
			report.ValidationFailures++
			return fmt.Errorf("codex import: invalid role %q at message %d", msg.Role, i)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tool mapping table
// ---------------------------------------------------------------------------

// codexResolveChordTool maps a Codex tool call onto a real Chord tool and its
// normalized arguments, reporting whether a mapping exists.
//
// A Codex tool call is not directly replayable: the current CLI emits one
// freeform `exec` tool that orchestrates the real work as nested calls, so the
// mapping has to come from the analyzed script (tc.ExecScript), and Codex's
// multi-agent tools live in a namespace Chord does not have. Anything that does
// not resolve to exactly one known Chord tool keeps the source tool call, under
// the name and arguments it was issued with, rather than a fabricated call the
// provider never declared.
func codexResolveChordTool(tc *codexToolCall) (chordName string, args json.RawMessage, mapped bool) {
	if codexIsExecToolName(tc.Name) {
		return codexResolveExecTool(tc)
	}
	if strings.EqualFold(strings.TrimSpace(tc.Namespace), codexCollaborationNamespace) {
		return "", nil, false
	}
	resolved, normalized, ok := resolveImportedTool(tc.Name, tc.Arguments)
	if !ok {
		return "", nil, false
	}
	return resolved, normalized, true
}

// codexResolveExecTool maps the single nested call of an `exec` script onto the
// Chord tool that performed the same work.
func codexResolveExecTool(tc *codexToolCall) (string, json.RawMessage, bool) {
	if tc.ExecScript == nil {
		return "", nil, false
	}
	switch tc.ExecScript.Nested {
	case codexNestedExecCommand:
		args := codexNormalizeExecCommandArgs(tc.ExecScript.Args)
		if args == nil {
			return "", nil, false
		}
		return tools.NameShell, args, true
	case codexNestedApplyPatch:
		args := normalizeApplyPatchArgs(map[string]any{"input": tc.ExecScript.Input})
		if args == nil {
			return "", nil, false
		}
		return tools.NameApplyPatch, args, true
	default:
		// write_stdin drives an interactive session Chord does not keep, and any
		// other nested tool is not one Chord declares.
		return "", nil, false
	}
}

// codexNormalizeExecCommandArgs maps a nested `exec_command` argument object
// onto Chord's shell schema.
//
// Codex's remaining knobs describe how the host ran the command rather than
// what it was — `shell`, `login`, `tty`, `sandbox_permissions`,
// `additional_permissions`, `justification`, `prefix_rule` — and
// `yield_time_ms` is a host yield window with a different default in Chord, so
// none of them carry over. A description carries over only when the source
// recorded one: a synthesized placeholder would sit where a live call shows
// the command's first line, marking the call as imported for a reader — user
// or model — that has no use for that fact.
func codexNormalizeExecCommandArgs(args map[string]any) json.RawMessage {
	cmd := codexPickString(args, "cmd", "command")
	if cmd == "" {
		return nil
	}
	result := map[string]any{
		"command": cmd,
	}
	if desc := codexPickString(args, "description"); desc != "" {
		result["description"] = desc
	}
	if wd := codexPickString(args, "workdir", "cwd"); wd != "" {
		result["workdir"] = wd
	}
	if ms := codexPickFloat(args, "timeout_ms"); ms > 0 {
		result["timeout_ms"] = int(ms)
	}
	b, _ := json.Marshal(result)
	return b
}

func buildImportedApplyPatchEnvelope(path, patch string) string {
	return "*** Begin Patch\n*** Update File: " + path + "\n" + strings.TrimRight(patch, "\r\n") + "\n*** End Patch"
}

func normalizeImportedShellArgs(args map[string]any) json.RawMessage {
	cmd := codexPickString(args, "cmd", "command")
	if cmd == "" {
		return nil
	}
	result := map[string]any{"command": cmd}
	// A description the source recorded (OpenCode's bash tool carries one) is
	// the call's own subject and survives verbatim; without it the Chord
	// header falls back to the command's first line, exactly like a live call.
	if desc := codexPickString(args, "description"); desc != "" {
		result["description"] = desc
	}
	if wd := codexPickString(args, "workdir", "cwd"); wd != "" {
		result["workdir"] = wd
	}
	if timeout := codexPickFloat(args, "timeout"); timeout > 0 {
		// Codex records the shell timeout in seconds; Chord's shell takes
		// timeout_ms, so convert to keep the imported deadline intact.
		result["timeout_ms"] = int(timeout * 1000)
	}
	b, _ := json.Marshal(result)
	return b
}

func normalizeImportedReadArgs(args map[string]any) json.RawMessage {
	path := codexPickString(args, "path", "file_path", "filePath", "file")
	if path == "" {
		return nil
	}
	result := map[string]any{"path": path}
	if offset := codexPickFloat(args, "offset", "start_line"); offset > 0 {
		result["offset"] = int(offset)
	}
	if limit := codexPickFloat(args, "limit", "max_lines", "max_output_tokens"); limit > 0 {
		result["limit"] = int(limit)
	}
	b, _ := json.Marshal(result)
	return b
}

func normalizeImportedWriteArgs(args map[string]any) json.RawMessage {
	path := codexPickString(args, "path", "file_path", "filePath", "file")
	if path == "" {
		return nil
	}
	content, ok := importedPickRequiredString(args, "content", "data", "text")
	if !ok {
		return nil
	}
	result := map[string]any{"path": path, "content": content}
	b, _ := json.Marshal(result)
	return b
}

func normalizeImportedDeleteArgs(args map[string]any) json.RawMessage {
	paths := codexPickStringList(args, "paths", "path", "file_path", "file")
	if len(paths) == 0 {
		return nil
	}
	reason := codexPickString(args, "reason")
	if reason == "" {
		reason = deleteReasonFallback
	}
	result := map[string]any{"paths": paths, "reason": reason}
	b, _ := json.Marshal(result)
	return b
}

func normalizeImportedGrepArgs(args map[string]any) json.RawMessage {
	pattern := codexPickString(args, "pattern", "query", "regex")
	if pattern == "" {
		return nil
	}
	result := map[string]any{"pattern": pattern}
	if paths := codexPickStringList(args, "paths", "path", "directory"); len(paths) > 0 {
		result["paths"] = paths
	}
	if includes := codexPickStringList(args, "includes", "glob", "file_pattern", "include"); len(includes) > 0 {
		result["includes"] = includes
	}
	b, _ := json.Marshal(result)
	return b
}

func normalizeImportedGlobArgs(args map[string]any) json.RawMessage {
	patterns := codexPickStringList(args, "patterns", "pattern", "glob")
	if len(patterns) == 0 {
		return nil
	}
	result := map[string]any{"patterns": patterns}
	if path := codexPickString(args, "path", "directory"); path != "" {
		result["path"] = path
	}
	b, _ := json.Marshal(result)
	return b
}

// codexPickString returns the first non-empty string value from the given keys.
func codexPickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

// codexPickFloat returns the first non-zero float64 value from the given keys.
func codexPickFloat(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				if n > 0 {
					return n
				}
			case int:
				if n > 0 {
					return float64(n)
				}
			}
		}
	}
	return 0
}

func codexPickStringList(m map[string]any, keys ...string) []string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return []string{s}
			}
		case []any:
			out := make([]string, 0, len(t))
			for _, item := range t {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, strings.TrimSpace(s))
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Unmapped tool calls and orphaned results
// ---------------------------------------------------------------------------

// codexImportedToolCall returns a Codex tool call that has no Chord mapping
// exactly as the rollout recorded it: the source tool name and its own
// arguments.
//
// The name and arguments are preserved rather than rewritten because the call
// really ran in the source session, so it is history the model should see as
// the action it was. Replacing it with a text note would drop the action from
// the trajectory, and wrapping the arguments in importer metadata would show
// bookkeeping instead of the original call.
func codexImportedToolCall(tc *codexToolCall) (string, json.RawMessage) {
	name := strings.TrimSpace(tc.Name)
	if name == "" {
		name = "unknown"
	}
	if len(tc.Arguments) > 0 {
		// A function_call carries its own argument object. A truncated or
		// malformed arguments string must not reach json.Marshal as an
		// invalid json.RawMessage (it would fail the whole import), so wrap
		// it into a legal JSON object that preserves the original text.
		if json.Valid(tc.Arguments) {
			return name, tc.Arguments
		}
		wrapped, err := json.Marshal(map[string]string{"input": string(tc.Arguments)})
		if err == nil {
			return name, wrapped
		}
		// Fall through to the safe default below.
	}
	if tc.ExecInput != "" {
		// A freeform custom tool carries its whole program as one string
		// parameter, which is the shape the source sent. The input always
		// stays: it is what the card shows, because a cell that reaches this
		// point has no Chord tool to render it under.
		payload := map[string]any{"input": tc.ExecInput}
		// Derived here rather than carried on the call: only a cell that
		// reaches this point lists its commands, and a cell that mapped to a
		// Chord tool never reads them.
		if commands := codexExecCommands(tc.ExecInput); len(commands) > 0 {
			// One command per line, so the card lists the batch instead of
			// encoding it as a JSON array. A single string keeps this on the
			// tool's existing multi-line argument path; the TUI renders a
			// list-shaped argument of its own (grep paths, delete paths)
			// with its own comma-joined form, which a raw array would
			// collide with. "commands" sorts before "input", so the folded
			// header previews the first command rather than the array
			// declaration the script opens with.
			payload["commands"] = strings.Join(commands, "\n")
		}
		args, err := json.Marshal(payload)
		if err == nil {
			return name, args
		}
	}
	return name, json.RawMessage(`{}`)
}

// codexImportedNoteMaxBytes bounds an imported fallback field so one record
// cannot dominate the transcript.
const codexImportedNoteMaxBytes = 4000

// codexImportedTruncatedSuffix marks a field cut to the imported-note budget.
// It is a named constant because budget accounting has to subtract its length
// before calling the truncator.
const codexImportedTruncatedSuffix = "\n...[truncated]"

// codexTruncateImportedText cuts text to a byte budget without splitting a
// UTF-8 rune, so an imported fallback never carries an invalid byte sequence.
func codexTruncateImportedText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + codexImportedTruncatedSuffix
}

func codexOrphanedToolResultFallback(tr *codexToolResult) message.Message {
	return message.Message{
		Role: message.RoleAssistant,
		Content: fmt.Sprintf("[unpaired tool result]\nCall ID: %s\nOutput:\n%s",
			tr.CallID, codexTruncateImportedText(tr.Output, codexImportedNoteMaxBytes)),
		Provenance: importedCodexProvenance(),
	}
}
