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
// Handles both the current protocol format (type+payload) and legacy
// item-based format used by older Codex versions.
func parseCodexJSONL(data []byte) ([]codexRolloutEntry, string, error) {
	// ---------------------------------------------------------------------------
	// Stage 1a: JSONL scanning + legacy/current schema normalization
	// ---------------------------------------------------------------------------

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

		// First, parse as raw map to detect format.
		var lineObj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &lineObj); err != nil {
			return nil, "", fmt.Errorf("codex import: line %d: parse JSON: %w", lineNo, err)
		}

		// Current format: top-level "type" + "payload" fields.
		if rawType, ok := lineObj["type"]; ok {
			var typeStr string
			if err := json.Unmarshal(rawType, &typeStr); err == nil && typeStr != "" {
				payload := lineObj["payload"]
				entries = append(entries, codexRolloutEntry{
					Timestamp: pickJSONString(lineObj, "timestamp"),
					EventType: typeStr,
					Payload:   payload,
				})
				// Extract session_id from session_meta entries.
				if sessionID == "" && typeStr == "session_meta" && len(payload) > 0 {
					var meta struct {
						ID string `json:"id"`
					}
					if json.Unmarshal(payload, &meta) == nil && meta.ID != "" {
						sessionID = meta.ID
					}
				}
				continue
			}
		}

		// Legacy format: "item" field with various inner structures.
		itemRaw, hasItem := lineObj["item"]
		if !hasItem || len(bytes.TrimSpace(itemRaw)) == 0 {
			continue
		}
		var itemObj map[string]json.RawMessage
		if err := json.Unmarshal(itemRaw, &itemObj); err != nil {
			continue
		}

		ts := pickJSONString(lineObj, "timestamp")

		// Extract session_id from legacy items.
		if sessionID == "" {
			if sid, ok := pickJSONStringOK(itemObj, "session_id", "sessionId", "sessionID", "id"); ok {
				sessionID = sid
			}
		}

		// Detect item kind from the legacy object.
		kind := strings.ToLower(strings.TrimSpace(pickJSONString(itemObj, "type")))

		// ---------------------------------------------------------------------------
		// Stage 1b: legacy item adapters + shared raw JSON helpers
		// ---------------------------------------------------------------------------

		// Legacy FunctionCall/FunctionCallOutput keys.
		if fcRaw, ok := itemObj["FunctionCall"]; ok {
			entries = append(entries, codexRolloutEntry{
				Timestamp: ts,
				EventType: "response_item",
				Payload:   codexLegacyFunctionCallToPayload(fcRaw),
			})
			continue
		}
		if fcoRaw, ok := itemObj["FunctionCallOutput"]; ok {
			entries = append(entries, codexRolloutEntry{
				Timestamp: ts,
				EventType: "response_item",
				Payload:   codexLegacyFunctionCallOutputToPayload(fcoRaw),
			})
			continue
		}
		if ctcRaw, ok := itemObj["CustomToolCall"]; ok {
			entries = append(entries, codexRolloutEntry{
				Timestamp: ts,
				EventType: "response_item",
				Payload:   codexLegacyCustomToolCallToPayload(ctcRaw),
			})
			continue
		}
		if ctcoRaw, ok := itemObj["CustomToolCallOutput"]; ok {
			entries = append(entries, codexRolloutEntry{
				Timestamp: ts,
				EventType: "response_item",
				Payload:   codexLegacyCustomToolCallOutputToPayload(ctcoRaw),
			})
			continue
		}

		switch kind {
		case "responseitem", "response_item":
			// Convert to response_item entry.
			role := strings.ToLower(strings.TrimSpace(pickJSONString(itemObj, "role")))
			text := pickJSONString(itemObj, "content", "text")
			if role != "" {
				payload := map[string]any{
					"type":    "message",
					"role":    role,
					"content": []map[string]string{{"type": "input_text", "text": text}},
				}
				payloadBytes, _ := json.Marshal(payload)
				entries = append(entries, codexRolloutEntry{Timestamp: ts, EventType: "response_item", Payload: payloadBytes})
			}

		case "eventmsg", "event_msg":
			eventType := strings.ToLower(strings.TrimSpace(pickJSONString(itemObj, "type", "kind")))
			if eventType == "" || eventType == "eventmsg" || eventType == "event_msg" {
				eventType = pickJSONString(itemObj, "__event_type")
			}
			// Try to determine event sub-type from the item content.
			if role := pickJSONString(itemObj, "role"); role == "user" {
				payload := map[string]any{"type": "user_message", "message": pickJSONString(itemObj, "content", "text", "message")}
				payloadBytes, _ := json.Marshal(payload)
				entries = append(entries, codexRolloutEntry{Timestamp: ts, EventType: "event_msg", Payload: payloadBytes})
			} else if role == "assistant" || eventType == "agent_message" {
				payload := map[string]any{"type": "agent_message", "message": pickJSONString(itemObj, "content", "text", "message")}
				payloadBytes, _ := json.Marshal(payload)
				entries = append(entries, codexRolloutEntry{Timestamp: ts, EventType: "event_msg", Payload: payloadBytes})
			}

		case "reasoning":
			text := pickJSONString(itemObj, "content", "text", "reasoning")
			payload := map[string]any{
				"type":    "reasoning",
				"content": []map[string]string{{"type": "summary_text", "text": text}},
			}
			payloadBytes, _ := json.Marshal(payload)
			entries = append(entries, codexRolloutEntry{Timestamp: ts, EventType: "response_item", Payload: payloadBytes})

		default:
			// Try to extract a message-like item from the legacy object.
			role := strings.ToLower(strings.TrimSpace(pickJSONString(itemObj, "role", "sender")))
			text := pickJSONString(itemObj, "content", "text", "message")
			if role == "user" || role == "assistant" {
				payload := map[string]any{
					"type":    "message",
					"role":    role,
					"content": []map[string]string{{"type": "input_text", "text": text}},
				}
				payloadBytes, _ := json.Marshal(payload)
				entries = append(entries, codexRolloutEntry{Timestamp: ts, EventType: "response_item", Payload: payloadBytes})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, "", fmt.Errorf("codex import: scan JSONL: %w", err)
	}
	return entries, sessionID, nil
}

// codexLegacyFunctionCallToPayload converts a legacy FunctionCall object to a
// response_item payload compatible with the new protocol.
func codexLegacyFunctionCallToPayload(fcRaw json.RawMessage) json.RawMessage {
	var fc struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		CallID    string          `json:"call_id"`
	}
	if err := json.Unmarshal(fcRaw, &fc); err != nil {
		return fcRaw
	}
	if fc.CallID == "" {
		fc.CallID = "call_legacy_" + fc.Name
	}
	// In the legacy format, arguments is a JSON object.
	// In the new protocol format, arguments is a JSON string containing JSON.
	// We need to convert: object → JSON-encoded string.
	var argsStr string
	if len(fc.Arguments) > 0 {
		goStr := string(fc.Arguments)
		jsonStr, err := json.Marshal(goStr)
		if err == nil {
			argsStr = string(jsonStr)
			argsStr = argsStr[1 : len(argsStr)-1] // strip outer quotes
		}
	}
	// Use a struct with a custom arguments field to ensure proper JSON encoding.
	type fcPayload struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments,omitempty"`
	}
	payload, _ := json.Marshal(fcPayload{
		Type:      "function_call",
		Name:      fc.Name,
		CallID:    fc.CallID,
		Arguments: argsStr,
	})
	return payload
}

// codexLegacyFunctionCallOutputToPayload converts a legacy FunctionCallOutput
// object to a response_item payload.
func codexLegacyFunctionCallOutputToPayload(fcoRaw json.RawMessage) json.RawMessage {
	var fco struct {
		Output interface{} `json:"output"`
		CallID string      `json:"call_id"`
	}
	if err := json.Unmarshal(fcoRaw, &fco); err != nil {
		return fcoRaw
	}
	if fco.CallID == "" {
		fco.CallID = "call_legacy_output"
	}
	// Normalize output to string.
	outputBytes, _ := json.Marshal(fco.Output)
	payload, _ := json.Marshal(map[string]any{
		"type":    "function_call_output",
		"output":  string(outputBytes),
		"call_id": fco.CallID,
	})
	return payload
}

// codexLegacyCustomToolCallToPayload converts a legacy CustomToolCall.
func codexLegacyCustomToolCallToPayload(ctcRaw json.RawMessage) json.RawMessage {
	var ctc struct {
		Name   string `json:"name"`
		CallID string `json:"call_id"`
		Input  string `json:"input"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(ctcRaw, &ctc); err != nil {
		return ctcRaw
	}
	if ctc.CallID == "" {
		ctc.CallID = "call_legacy_custom_" + ctc.Name
	}
	payload, _ := json.Marshal(map[string]any{
		"type":    "custom_tool_call",
		"name":    ctc.Name,
		"call_id": ctc.CallID,
		"input":   ctc.Input,
		"status":  ctc.Status,
	})
	return payload
}

// codexLegacyCustomToolCallOutputToPayload converts a legacy CustomToolCallOutput.
func codexLegacyCustomToolCallOutputToPayload(ctcoRaw json.RawMessage) json.RawMessage {
	var ctco struct {
		CallID string      `json:"call_id"`
		Output interface{} `json:"output"`
		Name   string      `json:"name"`
	}
	if err := json.Unmarshal(ctcoRaw, &ctco); err != nil {
		return ctcoRaw
	}
	if ctco.CallID == "" {
		ctco.CallID = "call_legacy_custom_output"
	}
	outputBytes, _ := json.Marshal(ctco.Output)
	payload, _ := json.Marshal(map[string]any{
		"type":    "custom_tool_call_output",
		"call_id": ctco.CallID,
		"output":  string(outputBytes),
	})
	return payload
}

// pickJSONString extracts a string value from a map of raw JSON messages.
func pickJSONString(m map[string]json.RawMessage, keys ...string) string {
	s, _ := pickJSONStringOK(m, keys...)
	return s
}

// pickJSONStringOK extracts a string value from a map of raw JSON messages,
// returning whether a non-empty value was found.
func pickJSONStringOK(m map[string]json.RawMessage, keys ...string) (string, bool) {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			s = strings.TrimSpace(s)
			if s != "" {
				return s, true
			}
		}
	}
	return "", false
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
// ---------------------------------------------------------------------------
// Stage 2: Build IR from normalized rollout entries
// ---------------------------------------------------------------------------

func buildCodexIR(entries []codexRolloutEntry, reasoningMode string, report *ImportReport) ([]*codexTurn, error) {
	// Phase A: Collect all response_items into turns.
	// We first scan for turn_context boundaries, then assign items.

	// Track current turn_id from turn_context / turn_started events.
	var currentTurnID string
	turnOrder := make(map[string]int) // turn_id -> order
	var turnOrderList []string

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

	// Create turn structs.
	turns := make(map[string]*codexTurn)
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
		turns[turnID] = t
		if _, exists := turnOrder[turnID]; !exists {
			turnOrder[turnID] = len(turnOrderList)
			turnOrderList = append(turnOrderList, turnID)
		}
		return t
	}

	// Global source order counter.
	sourceOrder := 0

	// Track response_item message roles to avoid duplicates from event_msg.
	// Maps turn_id -> set of roles seen from response_item.
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

	// Phase B: Process entries in source order.
	for _, e := range entries {
		switch e.EventType {
		case "response_item":
			if err := codexProcessResponseItem(e.Payload, currentTurnID, reasoningMode, getOrCreateTurn, recordResponseItemRole, &sourceOrder, report); err != nil {
				// Log but don't fail on individual item errors.
				report.warnf("response_item parse error: %v", err)
			}

		case "turn_context":
			var tc struct {
				TurnID string `json:"turn_id"`
			}
			if err := json.Unmarshal(e.Payload, &tc); err == nil && tc.TurnID != "" {
				currentTurnID = tc.TurnID
				getOrCreateTurn(currentTurnID) // ensure it exists
			}

		case "event_msg":
			codexProcessEventMsg(e.Payload, currentTurnID, getOrCreateTurn, hasResponseItemRole, &sourceOrder, report)
		}
	}

	// Phase C: Deduplicate event_msg entries that have response_item
	// counterparts in the same turn. response_item is the canonical source.
	for _, t := range turns {
		hasResponseUser := false
		hasResponseAssistant := false
		for _, m := range t.UserMessages {
			if m.Source == "response_item" {
				hasResponseUser = true
				break
			}
		}
		for _, m := range t.AssistantMessages {
			if m.Source == "response_item" {
				hasResponseAssistant = true
				break
			}
		}
		if hasResponseUser {
			filtered := t.UserMessages[:0]
			for _, m := range t.UserMessages {
				if m.Source == "event_msg" {
					report.SkippedDuplicates++
					report.SkippedEntries++
				} else {
					filtered = append(filtered, m)
				}
			}
			t.UserMessages = filtered
		}
		if hasResponseAssistant {
			filtered := t.AssistantMessages[:0]
			for _, m := range t.AssistantMessages {
				if m.Source == "event_msg" {
					report.SkippedDuplicates++
					report.SkippedEntries++
				} else {
					filtered = append(filtered, m)
				}
			}
			t.AssistantMessages = filtered
		}
	}

	// Phase D: Assemble turns in order.
	// If no turns were created, create a single default turn.
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
	currentTurnID string,
	reasoningMode string,
	getOrCreateTurn func(string) *codexTurn,
	recordResponseItemRole func(string, string),
	sourceOrder *int,
	report *ImportReport,
) error {
	var item struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
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

	turn := getOrCreateTurn(currentTurnID)
	*sourceOrder++

	switch item.Type {
	case "message":
		role := strings.ToLower(item.Role)
		if role == "developer" || role == "system" {
			report.SkippedEntries++
			return nil
		}
		text := codexExtractContentText(item.Content)
		if strings.TrimSpace(text) == "" {
			report.SkippedEntries++
			return nil
		}
		recordResponseItemRole(currentTurnID, role)
		if role == "user" {
			turn.UserMessages = append(turn.UserMessages, codexMessageItem{
				Role:    "user",
				Content: text,
				Source:  "response_item",
			})
		} else {
			turn.AssistantMessages = append(turn.AssistantMessages, codexMessageItem{
				Role:    "assistant",
				Content: text,
				Source:  "response_item",
			})
		}

	case "function_call":
		callID := item.CallID
		if callID == "" {
			report.SkippedEntries++
			report.warnf("function_call without call_id, skipped")
			return nil
		}
		turn.ToolCalls[callID] = &codexToolCall{
			CallID:      callID,
			Name:        item.Name,
			Arguments:   json.RawMessage(item.Args),
			TurnID:      currentTurnID,
			SourceOrder: *sourceOrder,
		}

	case "function_call_output":
		callID := item.CallID
		if callID == "" {
			report.SkippedEntries++
			report.warnf("function_call_output without call_id, skipped")
			return nil
		}
		outputText := codexExtractOutputText(item.Output)
		turn.ToolResults[callID] = &codexToolResult{
			CallID:      callID,
			Output:      outputText,
			TurnID:      currentTurnID,
			SourceOrder: *sourceOrder,
		}

	case "custom_tool_call":
		callID := item.CallID
		if callID == "" {
			report.SkippedEntries++
			return nil
		}
		// Custom tool calls are stored as "function_call" equivalents
		// but with name from the custom tool. We map apply_patch and
		// other known custom tools to the tool mapping table.
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
			TurnID:      currentTurnID,
			SourceOrder: *sourceOrder,
		}

	case "custom_tool_call_output":
		callID := item.CallID
		if callID == "" {
			report.SkippedEntries++
			return nil
		}
		outputText := codexExtractOutputText(item.Output)
		turn.ToolResults[callID] = &codexToolResult{
			CallID:      callID,
			Output:      outputText,
			TurnID:      currentTurnID,
			SourceOrder: *sourceOrder,
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
				TurnID:      currentTurnID,
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
	currentTurnID string,
	getOrCreateTurn func(string) *codexTurn,
	hasResponseItemRole func(string, string) bool,
	sourceOrder *int,
	report *ImportReport,
) {
	var ev struct {
		Type    string `json:"type"`
		TurnID  string `json:"turn_id"`
		Message string `json:"message"`
		Text    string `json:"text"`
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

	turnID := currentTurnID
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
			Role:    "assistant",
			Content: text,
			Source:  "event_msg",
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
			Role:    "user",
			Content: text,
			Source:  "event_msg",
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

	case "task_started", "turn_started", "task_complete", "turn_complete",
		"patch_apply_end", "patch_apply_begin", "web_search_end", "web_search_begin",
		"exec_command_begin", "exec_command_end", "exec_command_output_delta",
		"mcp_tool_call_begin", "mcp_tool_call_end", "mcp_startup_update", "mcp_startup_complete",
		"image_generation_begin", "image_generation_end",
		"context_compacted", "error", "warning", "turn_aborted", "turn_diff",
		"model_reroute", "model_verification", "stream_error", "deprecation_notice":
		// Supplemental/status events – skip silently.
		report.SkippedEntries++

	default:
		report.SkippedEntries++
	}
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

// codexProvenance returns the standard provenance for imported Codex messages.
func codexProvenance() *message.MessageProvenance {
	return &message.MessageProvenance{
		Source:     "import:codex",
		ProviderID: "openai",
		WireFamily: "openai-responses",
		Imported:   true,
	}
}

// linearizeCodexTurns converts reconstructed turns into a linear sequence
// of Chord messages suitable for transcript restore and context replay.
//
// Output ordering per turn:
// 1. Canonical user message(s)
// 2. Assistant tool-call message(s)
// 3. Corresponding tool-result message(s)
// 4. Assistant plain-text response(s) (after tool execution)
// ---------------------------------------------------------------------------
// Stage 3: Linearize IR into Chord messages
// ---------------------------------------------------------------------------

func linearizeCodexTurns(turns []*codexTurn, report *ImportReport) []message.Message {
	var out []message.Message

	for _, turn := range turns {
		// 1. Emit user messages.
		for _, um := range turn.UserMessages {
			out = append(out, message.Message{
				Role:       "user",
				Content:    um.Content,
				Provenance: codexProvenance(),
			})
		}

		// 2. Emit structured tool calls + results, paired by call_id.
		//    Build ordered list of tool call IDs by source order.
		type toolCallEntry struct {
			callID string
			order  int
		}
		var orderedCalls []toolCallEntry
		for callID, tc := range turn.ToolCalls {
			orderedCalls = append(orderedCalls, toolCallEntry{callID: callID, order: tc.SourceOrder})
		}
		// Sort by source order.
		for i := 0; i < len(orderedCalls); i++ {
			for j := i + 1; j < len(orderedCalls); j++ {
				if orderedCalls[j].order < orderedCalls[i].order {
					orderedCalls[i], orderedCalls[j] = orderedCalls[j], orderedCalls[i]
				}
			}
		}

		for _, entry := range orderedCalls {
			tc := turn.ToolCalls[entry.callID]
			tr := turn.ToolResults[entry.callID]

			chordName, ok := codexToolMapping[tc.Name]
			if !ok {
				// Unsupported tool: downgrade to text fallback.
				out = append(out, codexUnsupportedToolCallFallback(tc))
				report.UnsupportedToolCalls++
				if tr != nil {
					out = append(out, codexUnsupportedToolResultFallback(tr, tc.Name))
					report.UnsupportedToolResults++
					delete(turn.ToolResults, entry.callID) // mark as consumed
				}
				continue
			}

			// Map the tool call to Chord structured format.
			normArgs := codexNormalizeToolArgs(tc.Name, chordName, tc.Arguments)
			if normArgs == nil {
				// Normalization failed: downgrade.
				out = append(out, codexUnsupportedToolCallFallback(tc))
				report.UnsupportedToolCalls++
				if tr != nil {
					out = append(out, codexUnsupportedToolResultFallback(tr, tc.Name))
					report.UnsupportedToolResults++
					delete(turn.ToolResults, entry.callID)
				}
				continue
			}

			out = append(out, message.Message{
				Role: "assistant",
				ToolCalls: []message.ToolCall{
					{
						ID:   tc.CallID,
						Name: chordName,
						Args: normArgs,
					},
				},
				Provenance: codexProvenance(),
			})
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
					Provenance: codexProvenance(),
				})
				report.StructuredToolResults++
				delete(turn.ToolResults, entry.callID) // mark as consumed
			}
		}

		// 3. Emit any orphaned tool results that weren't paired above.
		for _, tr := range turn.ToolResults {
			// No matching tool call was imported structurally.
			out = append(out, codexOrphanedToolResultFallback(tr))
			report.MissingToolDeclarations++
		}

		// 4. Emit assistant messages.
		for _, am := range turn.AssistantMessages {
			out = append(out, message.Message{
				Role:       "assistant",
				Content:    am.Content,
				Provenance: codexProvenance(),
			})
		}
	}

	// Filter out empty messages.
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
	// Keep the legacy aggregate field populated for older tooling that only
	// understands text-mode tool-entry counts.
	report.ToolEntriesRendered = report.StructuredToolCalls + report.StructuredToolResults +
		report.UnsupportedToolCalls + report.UnsupportedToolResults + report.MissingToolDeclarations

	return filtered
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
	"write_file":   "Write",
	"file_write":   "Write",
	"edit_file":    "Edit",
	"file_edit":    "Edit",
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
		Provenance: codexProvenance(),
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
		Provenance: codexProvenance(),
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
		Provenance: codexProvenance(),
	}
}
