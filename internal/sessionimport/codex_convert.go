package sessionimport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// ---------------------------------------------------------------------------
// Stage 1: Parse JSONL → Codex IR
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
		if err := json.Unmarshal([]byte(line), &lineObj); err != nil {
			return nil, "", fmt.Errorf("codex import: line %d: parse JSON: %w", lineNo, err)
		}

		rawType, ok := lineObj["type"]
		if !ok {
			continue
		}
		var typeStr string
		if err := json.Unmarshal(rawType, &typeStr); err != nil || typeStr == "" {
			continue
		}
		payload := lineObj["payload"]
		entries = append(entries, codexRolloutEntry{
			Timestamp: pickJSONString(lineObj, "timestamp"),
			EventType: typeStr,
			Payload:   payload,
		})
		if sessionID == "" && typeStr == "session_meta" && len(payload) > 0 {
			var meta struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(payload, &meta) == nil && meta.ID != "" {
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
		if err := json.Unmarshal(raw, &s); err == nil {
			s = strings.TrimSpace(s)
			if s != "" {
				return s
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Stage 2: Build IR from parsed entries
// ---------------------------------------------------------------------------

// buildCodexIR transforms parsed rollout entries into an intermediate
// representation with reconstructed turns and structured tool lineage.
//
// Source precedence:
//   - response_item is the canonical content source
//   - event_msg supplements metadata (turn boundaries, usage, status)
//   - turn_context defines turn identity
func buildCodexIR(entries []codexRolloutEntry, reasoningMode string, report *ImportReport) ([]*codexTurn, error) {
	// Phase A: Collect all response_items into turns.
	// We first scan for turn_context boundaries, then assign items.

	// Track current turn_id from turn_context / turn_started events.
	var currentTurnID string
	turnOrder := make(map[string]int) // turn_id -> order
	var turnOrderList []string
	fallbackTurnSeq := 0

	// Pre-scan to identify turn boundaries.
	for _, e := range entries {
		switch e.EventType {
		case "turn_context":
			var tc struct {
				TurnID string `json:"turn_id"`
			}
			if err := json.Unmarshal(e.Payload, &tc); err == nil && tc.TurnID != "" {
				if _, exists := turnOrder[tc.TurnID]; !exists {
					turnOrder[tc.TurnID] = len(turnOrderList)
					turnOrderList = append(turnOrderList, tc.TurnID)
				}
			}
		case "event_msg":
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
			turnID = "_default"
		}
		if t, ok := turns[turnID]; ok {
			return t
		}
		t := &codexTurn{
			TurnID:      turnID,
			ToolCalls:   make(map[string]*codexToolCall),
			ToolResults: make(map[string]*codexToolResult),
		}
		if turnID != "_default" && strings.HasPrefix(turnID, "_fallback_") {
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
	responseItemRoles := make(map[string]map[string]bool)
	recordResponseItemRole := func(turnID, role string) {
		if _, ok := responseItemRoles[turnID]; !ok {
			responseItemRoles[turnID] = make(map[string]bool)
		}
		responseItemRoles[turnID][role] = true
	}
	hasResponseItemRole := func(turnID, role string) bool {
		if m, ok := responseItemRoles[turnID]; ok {
			return m[role]
		}
		return false
	}
	resolveActiveTurnID := func(role string) string {
		if currentTurnID != "" {
			return currentTurnID
		}
		if role == "user" {
			if t, ok := turns["_default"]; ok {
				hasAssistantActivity := len(t.AssistantMessages) > 0 || len(t.ToolCalls) > 0 || len(t.ToolResults) > 0
				if hasAssistantActivity {
					newID := newFallbackTurnID()
					currentTurnID = newID
					return newID
				}
			}
		}
		return "_default"
	}

	for _, e := range entries {
		switch e.EventType {
		case "response_item":
			if err := codexProcessResponseItem(e.Payload, resolveActiveTurnID, reasoningMode, getOrCreateTurn, recordResponseItemRole, callOwners, &sourceOrder, report); err != nil {
				report.warnf("response_item parse error: %v", err)
			}

		case "turn_context":
			var tc struct {
				TurnID string `json:"turn_id"`
			}
			if err := json.Unmarshal(e.Payload, &tc); err == nil && tc.TurnID != "" {
				currentTurnID = tc.TurnID
				turn := getOrCreateTurn(currentTurnID)
				turn.HasTurnContext = true
				turn.HasExplicitTurnID = true
			}

		case "event_msg":
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
	resolveActiveTurnID func(string) string,
	reasoningMode string,
	getOrCreateTurn func(string) *codexTurn,
	recordResponseItemRole func(string, string),
	callOwners map[string]string,
	sourceOrder *int,
	report *ImportReport,
) error {
	var item struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		TurnID  string          `json:"turn_id"`
		TurnID2 string          `json:"turnId"`
		Content json.RawMessage `json:"content"`
		Name    string          `json:"name"`
		CallID  string          `json:"call_id"`
		Args    string          `json:"arguments"`
		Output  json.RawMessage `json:"output"`
		Status  string          `json:"status"`
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
		turnID = resolveActiveTurnID(item.Role)
	}
	turn := getOrCreateTurn(turnID)
	if strings.TrimSpace(item.TurnID) != "" || strings.TrimSpace(item.TurnID2) != "" {
		turn.HasExplicitTurnID = true
	}
	*sourceOrder++

	switch item.Type {
	case "message":
		role := strings.ToLower(item.Role)
		if role == "developer" || role == "system" {
			report.SkippedEntries++
			return nil
		}
		text := codexExtractContentText(item.Content)
		if role == "user" && isCodexStartupInstructionMessage(text) {
			report.SkippedEntries++
			return nil
		}
		if strings.TrimSpace(text) == "" {
			report.SkippedEntries++
			return nil
		}
		recordResponseItemRole(turnID, role)
		if role == "user" {
			turn.UserMessages = append(turn.UserMessages, codexMessageItem{
				Role:        "user",
				Content:     text,
				Source:      "response_item",
				SourceOrder: *sourceOrder,
			})
		} else {
			turn.AssistantMessages = append(turn.AssistantMessages, codexMessageItem{
				Role:        "assistant",
				Content:     text,
				Source:      "response_item",
				SourceOrder: *sourceOrder,
			})
		}

	case "function_call":
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
			Arguments:   json.RawMessage(item.Args),
			TurnID:      turnID,
			SourceOrder: *sourceOrder,
		}
		callOwners[callID] = turnID

	case "function_call_output":
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

	case "custom_tool_call":
		callID := item.CallID
		if callID == "" {
			report.SkippedEntries++
			return nil
		}
		var inputStr string
		if err := json.Unmarshal(payload, &struct {
			Input *string `json:"input"`
		}{Input: &inputStr}); err != nil {
			inputStr = ""
		}
		argsJSON, _ := json.Marshal(map[string]string{
			"input": inputStr,
		})
		turn.ToolCalls[callID] = &codexToolCall{
			CallID:      callID,
			Name:        item.Name,
			Arguments:   argsJSON,
			TurnID:      turnID,
			SourceOrder: *sourceOrder,
		}
		callOwners[callID] = turnID

	case "custom_tool_call_output":
		callID := item.CallID
		if callID == "" {
			report.SkippedEntries++
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

	case "reasoning":
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
	resolveActiveTurnID func(string) string,
	getOrCreateTurn func(string) *codexTurn,
	hasResponseItemRole func(string, string) bool,
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
		if hasResponseItemRole(turnID, "assistant") {
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
			Role:        "assistant",
			Content:     text,
			Source:      "event_msg",
			SourceOrder: *sourceOrder,
		})

	case "user_message":
		if hasResponseItemRole(turnID, "user") {
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
			Role:        "user",
			Content:     text,
			Source:      "event_msg",
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
		if item.Source == "response_item" {
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
func linearizeCodexTurns(turns []*codexTurn, toolMode string, report *ImportReport) []message.Message {
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
			kind   string
			order  int
			user   codexMessageItem
			assist codexMessageItem
			callID string
		}
		var ordered []orderedItem
		for _, um := range turn.UserMessages {
			ordered = append(ordered, orderedItem{kind: "user", order: um.SourceOrder, user: um})
		}
		for _, am := range turn.AssistantMessages {
			ordered = append(ordered, orderedItem{kind: "assistant", order: am.SourceOrder, assist: am})
		}
		for callID, tc := range turn.ToolCalls {
			ordered = append(ordered, orderedItem{kind: "tool_call", order: tc.SourceOrder, callID: callID})
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
			case "user":
				out = append(out, message.Message{
					Role:       "user",
					Content:    item.user.Content,
					Provenance: importedCodexProvenance(),
				})
			case "assistant":
				msg := message.Message{
					Role:       "assistant",
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
			case "tool_call":
				tc := turn.ToolCalls[item.callID]
				tr := turn.ToolResults[item.callID]

				chordName, ok := codexToolMapping[tc.Name]
				if toolMode == ToolModeText {
					out = append(out, codexUnsupportedToolCallFallback(tc))
					report.UnsupportedToolCalls++
					if tr != nil {
						out = append(out, codexUnsupportedToolResultFallback(tr, tc.Name))
						report.UnsupportedToolResults++
						delete(turn.ToolResults, item.callID)
					}
					continue
				}
				if !ok {
					out = append(out, codexUnsupportedToolCallCard(tc, "no safe Chord mapping", reasoningText, usage))
					reasoningText = ""
					reasoningAttached = false
					usage = nil
					report.UnsupportedToolCalls++
					if tr != nil {
						out = append(out, codexToolResultMessage(tr))
						report.UnsupportedToolResults++
						delete(turn.ToolResults, item.callID)
					}
					continue
				}

				normArgs := codexNormalizeToolArgs(tc.Name, chordName, tc.Arguments)
				if normArgs == nil {
					out = append(out, codexUnsupportedToolCallCard(tc, "argument normalization failed", reasoningText, usage))
					reasoningText = ""
					reasoningAttached = false
					usage = nil
					report.UnsupportedToolCalls++
					if tr != nil {
						out = append(out, codexToolResultMessage(tr))
						report.UnsupportedToolResults++
						delete(turn.ToolResults, item.callID)
					}
					continue
				}

				out = append(out, message.Message{
					Role: "assistant",
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
				report.StructuredToolCalls++

				if tr != nil {
					status := "success"
					if tr.Status != "" {
						status = tr.Status
					}
					out = append(out, message.Message{
						Role:       "tool",
						ToolCallID: tr.CallID,
						Content:    tr.Output,
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
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			filtered = append(filtered, m)
			continue
		}
		if strings.TrimSpace(m.Content) != "" || len(m.Parts) > 0 {
			filtered = append(filtered, m)
		}
	}
	report.ToolEntriesRendered = report.StructuredToolCalls + report.StructuredToolResults +
		report.UnsupportedToolCalls + report.UnsupportedToolResults + report.MissingToolDeclarations

	return filtered
}

func validateImportedCodexMessages(msgs []message.Message, report *ImportReport) error {
	declared := make(map[string]struct{})
	for i, msg := range msgs {
		switch msg.Role {
		case "assistant":
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" || tc.Name == "" || len(tc.Args) == 0 {
					report.ValidationFailures++
					return fmt.Errorf("codex import: invalid structured tool call at message %d", i)
				}
				declared[tc.ID] = struct{}{}
			}
		case "tool":
			if msg.ToolCallID == "" {
				report.ValidationFailures++
				return fmt.Errorf("codex import: tool result missing tool_call_id at message %d", i)
			}
			if _, ok := declared[msg.ToolCallID]; !ok {
				report.ValidationFailures++
				return fmt.Errorf("codex import: tool result references undeclared tool_call_id %q", msg.ToolCallID)
			}
		case "user":
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

// codexToolMapping maps Codex tool names to Chord tool names.
// Only tools with high-confidence argument mapping are included.
// Tools not in this map will be downgraded to text fallback.
var codexToolMapping = map[string]string{
	"exec_command": "Shell",
	"shell":        "Shell",
	"read_file":    "Read",
	"file_read":    "Read",
	"grep":         "Grep",
	"search":       "Grep",
	"glob":         "Glob",
	"list_files":   "Glob",
}

// codexNormalizeToolArgs converts Codex tool arguments into the shape
// expected by Chord tools. Returns nil if normalization fails.
func codexNormalizeToolArgs(codexName string, chordName string, rawArgs json.RawMessage) json.RawMessage {
	if len(rawArgs) == 0 {
		return nil
	}

	// Parse raw arguments.
	var args map[string]any
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		// Try as a string (the Codex responses API sends arguments as a JSON string).
		var s string
		if err := json.Unmarshal(rawArgs, &s); err == nil {
			if err := json.Unmarshal([]byte(s), &args); err != nil {
				return nil
			}
		} else {
			return nil
		}
	}

	switch chordName {
	case "Shell":
		return codexNormalizeShellArgs(args)
	case "Read":
		return codexNormalizeReadArgs(args)
	case "Write":
		return codexNormalizeWriteArgs(args)
	case "Edit":
		return codexNormalizeEditArgs(args)
	case "Grep":
		return codexNormalizeGrepArgs(args)
	case "Glob":
		return codexNormalizeGlobArgs(args)
	default:
		return nil
	}
}

func codexNormalizeShellArgs(args map[string]any) json.RawMessage {
	cmd := codexPickString(args, "cmd", "command")
	if cmd == "" {
		return nil
	}
	result := map[string]any{
		"command":     cmd,
		"description": "Imported Codex shell command",
	}
	if wd := codexPickString(args, "workdir", "cwd"); wd != "" {
		result["workdir"] = wd
	}
	if timeout := codexPickFloat(args, "timeout"); timeout > 0 {
		result["timeout"] = int(timeout)
	}
	b, _ := json.Marshal(result)
	return b
}

func codexNormalizeReadArgs(args map[string]any) json.RawMessage {
	path := codexPickString(args, "path", "file_path", "file")
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

func codexNormalizeWriteArgs(args map[string]any) json.RawMessage {
	path := codexPickString(args, "path", "file_path", "file")
	content := codexPickString(args, "content", "data", "text")
	if path == "" || content == "" {
		return nil
	}
	result := map[string]any{"path": path, "content": content}
	b, _ := json.Marshal(result)
	return b
}

func codexNormalizeEditArgs(args map[string]any) json.RawMessage {
	path := codexPickString(args, "path", "file_path", "file")
	oldStr := codexPickString(args, "old_string", "old", "find")
	newStr := codexPickString(args, "new_string", "new", "replace")
	if path == "" || oldStr == "" {
		return nil
	}
	result := map[string]any{
		"path":       path,
		"old_string": oldStr,
		"new_string": newStr,
	}
	if replaceAll, ok := args["replace_all"]; ok {
		result["replace_all"] = replaceAll
	}
	b, _ := json.Marshal(result)
	return b
}

func codexNormalizeGrepArgs(args map[string]any) json.RawMessage {
	pattern := codexPickString(args, "pattern", "query", "regex")
	if pattern == "" {
		return nil
	}
	result := map[string]any{"pattern": pattern}
	if path := codexPickString(args, "path", "directory"); path != "" {
		result["path"] = path
	}
	if glob := codexPickString(args, "glob", "file_pattern", "include"); glob != "" {
		result["glob"] = glob
	}
	b, _ := json.Marshal(result)
	return b
}

func codexNormalizeGlobArgs(args map[string]any) json.RawMessage {
	pattern := codexPickString(args, "pattern", "glob", "path")
	if pattern == "" {
		return nil
	}
	result := map[string]any{"pattern": pattern}
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

// ---------------------------------------------------------------------------
// Fallback rendering for unsupported tools
// ---------------------------------------------------------------------------

func codexUnsupportedToolCallCard(tc *codexToolCall, reason string, reasoningText string, usage *message.TokenUsage) message.Message {
	name := strings.TrimSpace(tc.Name)
	if name == "" {
		name = "unknown"
	}
	args := map[string]any{
		"unsupported": true,
		"source":      "codex",
		"reason":      reason,
	}
	if len(tc.Arguments) > 0 {
		var v any
		if json.Unmarshal(tc.Arguments, &v) == nil {
			args["arguments"] = v
		} else {
			args["arguments"] = string(tc.Arguments)
		}
	}
	argsJSON, _ := json.Marshal(args)
	return message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   tc.CallID,
			Name: name,
			Args: argsJSON,
		}},
		ReasoningContent: reasoningText,
		Usage:            usage,
		Provenance:       importedCodexProvenance(),
	}
}

func codexToolResultMessage(tr *codexToolResult) message.Message {
	status := "success"
	if tr.Status != "" {
		status = tr.Status
	}
	return message.Message{
		Role:       "tool",
		ToolCallID: tr.CallID,
		Content:    tr.Output,
		ToolStatus: status,
		Provenance: importedCodexProvenance(),
	}
}

func codexUnsupportedToolCallFallback(tc *codexToolCall) message.Message {
	var argsPretty string
	if len(tc.Arguments) > 0 {
		var v any
		if json.Unmarshal(tc.Arguments, &v) == nil {
			b, _ := json.MarshalIndent(v, "", "  ")
			argsPretty = string(b)
		} else {
			argsPretty = string(tc.Arguments)
		}
	}
	content := fmt.Sprintf("[Imported unsupported tool call]\nTool: %s\nReason: no safe Chord mapping\nCall ID: %s", tc.Name, tc.CallID)
	if argsPretty != "" {
		content += "\nArguments:\n" + argsPretty
	}
	return message.Message{
		Role:       "assistant",
		Content:    content,
		Provenance: importedCodexProvenance(),
	}
}

func codexUnsupportedToolResultFallback(tr *codexToolResult, toolName string) message.Message {
	output := tr.Output
	if len(output) > 4000 {
		output = output[:4000] + "\n...[truncated]"
	}
	return message.Message{
		Role: "assistant",
		Content: fmt.Sprintf("[Imported unsupported tool result]\nTool: %s\nCall ID: %s\nOutput:\n%s",
			toolName, tr.CallID, output),
		Provenance: importedCodexProvenance(),
	}
}

func codexOrphanedToolResultFallback(tr *codexToolResult) message.Message {
	output := tr.Output
	if len(output) > 4000 {
		output = output[:4000] + "\n...[truncated]"
	}
	return message.Message{
		Role: "assistant",
		Content: fmt.Sprintf("[Imported orphaned tool result]\nCall ID: %s\nOutput:\n%s",
			tr.CallID, output),
		Provenance: importedCodexProvenance(),
	}
}
