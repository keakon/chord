package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/keakon/chord/internal/message"
)

var (
	thinkingToolcallMarkerRe = regexp.MustCompile(
		`<\|tool_(?:calls_section_begin|calls_section_end|call_begin|call_end|call_argument_begin|call_argument_end)\|>`,
	)
	thinkingToolcallFunctionLineRe = regexp.MustCompile(`(?s)functions\.[A-Za-z_][A-Za-z0-9_]*:(?:\d+)?(?:\s*\{.*?\})?`)
	thinkingToolcallExtraBlankRe   = regexp.MustCompile(`\n{3,}`)

	// parseThinkingFuncRe matches function header lines like "functions.Bash:6"
	// or "functions.Read:" (without index). Captures: (1) tool name, (2) optional index.
	parseThinkingFuncRe = regexp.MustCompile(`functions\.([A-Za-z_][A-Za-z0-9_]*):(\d*)`)

	// nonStdEscapeRe matches non-standard JSON escape sequences like \xHH that
	// models sometimes produce (JSON only supports \n \t \r \\ \" \/ \uXXXX).
	nonStdEscapeRe = regexp.MustCompile(`\\x([0-9a-fA-F]{2})`)
)

func scrubThinkingToolcallMarkers(text string) string {
	if text == "" {
		return ""
	}
	cleaned := thinkingToolcallMarkerRe.ReplaceAllString(text, "")
	cleaned = thinkingToolcallFunctionLineRe.ReplaceAllString(cleaned, "")
	cleaned = thinkingToolcallExtraBlankRe.ReplaceAllString(cleaned, "\n\n")
	return strings.TrimSpace(cleaned)
}

// parseThinkingToolcalls extracts pseudo tool calls from reasoning_content text.
//
// The expected format (observed from moonshotai/kimi-k2.5):
//
//	<|tool_calls_section_begin|>
//	<|tool_call_begin|> functions.Bash:0 <|tool_call_argument_begin|> {"command": "ls"} <|tool_call_end|>
//	<|tool_call_begin|> functions.Read:1 <|tool_call_argument_begin|> {"path": "foo.go"} <|tool_call_end|>
//	<|tool_calls_section_end|>
//
// Alternative formats also supported:
// - Functions followed by arguments without markers: "functions.Bash:0 {\"command\": \"ls\"}"
// - Incomplete sections (only end markers): "...} <|tool_call_end|> <|tool_calls_section_end|>"
//
// Whitespace and newlines between markers are inconsistent. The function
// extracts only the last tool_calls_section (the one the model intended to
// execute) and returns parsed ToolCall structs. Malformed entries are skipped.
func parseThinkingToolcalls(reasoning string) []message.ToolCall {
	if reasoning == "" {
		return nil
	}

	sectionBegin := "<|tool_calls_section_begin|>"
	sectionEnd := "<|tool_calls_section_end|>"

	lastBegin := strings.LastIndex(reasoning, sectionBegin)
	var section string
	if lastBegin >= 0 {
		section = reasoning[lastBegin+len(sectionBegin):]
		if idx := strings.Index(section, sectionEnd); idx >= 0 {
			section = section[:idx]
		}
	} else {
		section = reasoning
	}

	// Split on <|tool_call_begin|> to get individual call blocks.
	callBegin := "<|tool_call_begin|>"
	callEnd := "<|tool_call_end|>"
	argBegin := "<|tool_call_argument_begin|>"

	parts := strings.Split(section, callBegin)
	var calls []message.ToolCall
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Strip trailing <|tool_call_end|> and any content after it
		if idx := strings.Index(part, callEnd); idx >= 0 {
			part = part[:idx]
		}

		// Try to find arguments - either with marker or as raw JSON
		argIdx := strings.Index(part, argBegin)
		var header, argsStr string
		if argIdx >= 0 {
			// Standard format: "functions.ToolName:ID <|tool_call_argument_begin|> {JSON}"
			header = strings.TrimSpace(part[:argIdx])
			argsStr = strings.TrimSpace(part[argIdx+len(argBegin):])
		} else {
			// Alternative format: try to parse as "functions.ToolName:ID {JSON}"
			// Find where the JSON starts by looking for { character
			if braceIdx := strings.Index(part, "{"); braceIdx >= 0 {
				header = strings.TrimSpace(part[:braceIdx])
				argsStr = strings.TrimSpace(part[braceIdx:])
			} else {
				// No arguments found, skip this entry
				continue
			}
		}

		// Parse function header
		m := parseThinkingFuncRe.FindStringSubmatch(header)
		if m == nil {
			continue // unrecognized header format
		}
		toolName := m[1]
		toolIndex := m[2] // may be empty

		// Build ID matching the provider's convention: " functions.ToolName:Index"
		callID := fmt.Sprintf(" functions.%s:%s", toolName, toolIndex)

		// Validate args as JSON. Models sometimes emit non-standard escape
		// sequences (e.g. \xHH) that are invalid in JSON; normalise first.
		argsStr = fixNonStdJSONEscapes(argsStr)
		if !json.Valid([]byte(argsStr)) {
			// Try to recover: sometimes args span multiple lines with trailing noise.
			// Find the last '}' and truncate.
			if lastBrace := strings.LastIndex(argsStr, "}"); lastBrace >= 0 {
				argsStr = argsStr[:lastBrace+1]
			}
			if !json.Valid([]byte(argsStr)) {
				continue // still invalid, skip
			}
		}

		calls = append(calls, message.ToolCall{
			ID:   callID,
			Name: toolName,
			Args: json.RawMessage(argsStr),
		})
	}

	return calls
}

// fixNonStdJSONEscapes replaces non-standard \xHH escape sequences with the
// JSON-compliant \u00HH form. Models sometimes emit these in tool arguments
// (e.g. grep patterns containing raw bytes like \xe8\xa7\xa3).
func fixNonStdJSONEscapes(s string) string {
	return nonStdEscapeRe.ReplaceAllString(s, `\u00$1`)
}
