package agent

import (
	"strings"
	"testing"
)

func TestScrubThinkingToolcallMarkers(t *testing.T) {
	raw := strings.Join([]string{
		"analysis before",
		"<|tool_calls_section_begin|>",
		"functions.Bash:11",
		"<|tool_call_begin|>",
		"{}",
		"<|tool_call_end|>",
		"analysis after",
	}, "\n")

	got := scrubThinkingToolcallMarkers(raw)
	if strings.Contains(got, "<|tool_call_begin|>") {
		t.Fatal("expected marker to be removed")
	}
	if strings.Contains(got, "functions.Bash:11") {
		t.Fatal("expected function template line to be removed")
	}
	if !strings.Contains(got, "analysis before") || !strings.Contains(got, "analysis after") {
		t.Fatalf("expected surrounding reasoning text to remain, got %q", got)
	}
}

func TestScrubThinkingToolcallMarkers_MultilineInlineFunctions(t *testing.T) {
	raw := strings.Join([]string{
		"some analysis",
		"functions.Bash:18 {",
		"  \"cmd\": \"ls\"",
		"} more analysis",
	}, "\n")
	got := scrubThinkingToolcallMarkers(raw)
	if strings.Contains(got, "functions.Bash") {
		t.Fatal("expected multiline function block to be removed")
	}
	if !strings.Contains(got, "some analysis") || !strings.Contains(got, "more analysis") {
		t.Fatalf("expected surrounding reasoning text to remain, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// parseThinkingToolcalls tests
// ---------------------------------------------------------------------------

func TestParseThinkingToolcalls_SingleCall(t *testing.T) {
	// Real format observed from moonshotai/kimi-k2.5 logs.
	reasoning := ` <|tool_calls_section_begin|> <|tool_call_begin|>   functions.Bash:6 <|tool_call_argument_begin|> {"command": "git diff --staged", "description": "show staged diff"} <|tool_call_end|> <|tool_calls_section_end|>`

	calls := parseThinkingToolcalls(reasoning)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "Bash" {
		t.Fatalf("expected tool name Bash, got %q", calls[0].Name)
	}
	if calls[0].ID != " functions.Bash:6" {
		t.Fatalf("expected ID ' functions.Bash:6', got %q", calls[0].ID)
	}
	if !strings.Contains(string(calls[0].Args), "git diff --staged") {
		t.Fatalf("expected args to contain 'git diff --staged', got %s", calls[0].Args)
	}
}

func TestParseThinkingToolcalls_MultipleCallsInline(t *testing.T) {
	reasoning := `check if README has updates: <|tool_calls_section_begin|> <|tool_call_begin|> functions.Grep:2 <|tool_call_argument_begin|> {"pattern": "ctrl\\+p|SwitchModel", "path": "README.md"} <|tool_call_end|> <|tool_calls_section_end|>`

	calls := parseThinkingToolcalls(reasoning)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "Grep" {
		t.Fatalf("expected tool name Grep, got %q", calls[0].Name)
	}
}

func TestParseThinkingToolcalls_MultilineCalls(t *testing.T) {
	reasoning := strings.Join([]string{
		"analysis before",
		"<|tool_calls_section_begin|>",
		"<|tool_call_begin|> functions.Bash:0 <|tool_call_argument_begin|> {\"command\": \"ls\"} <|tool_call_end|>",
		"<|tool_call_begin|> functions.Read:1 <|tool_call_argument_begin|> {\"path\": \"foo.go\"} <|tool_call_end|>",
		"<|tool_calls_section_end|>",
	}, "\n")

	calls := parseThinkingToolcalls(reasoning)
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}
	if calls[0].Name != "Bash" {
		t.Fatalf("expected first tool Bash, got %q", calls[0].Name)
	}
	if calls[1].Name != "Read" {
		t.Fatalf("expected second tool Read, got %q", calls[1].Name)
	}
	if calls[0].ID != " functions.Bash:0" {
		t.Fatalf("expected first ID ' functions.Bash:0', got %q", calls[0].ID)
	}
	if calls[1].ID != " functions.Read:1" {
		t.Fatalf("expected second ID ' functions.Read:1', got %q", calls[1].ID)
	}
}

func TestParseThinkingToolcalls_NoSection(t *testing.T) {
	calls := parseThinkingToolcalls("just some normal thinking content without any tool calls")
	if len(calls) != 0 {
		t.Fatalf("expected 0 tool calls, got %d", len(calls))
	}
}

func TestParseThinkingToolcalls_EmptyString(t *testing.T) {
	calls := parseThinkingToolcalls("")
	if len(calls) != 0 {
		t.Fatalf("expected 0 tool calls, got %d", len(calls))
	}
}

func TestParseThinkingToolcalls_MalformedJSON(t *testing.T) {
	reasoning := `<|tool_calls_section_begin|> <|tool_call_begin|> functions.Bash:0 <|tool_call_argument_begin|> {invalid json <|tool_call_end|> <|tool_calls_section_end|>`

	calls := parseThinkingToolcalls(reasoning)
	if len(calls) != 0 {
		t.Fatalf("expected 0 tool calls for malformed JSON, got %d", len(calls))
	}
}

func TestParseThinkingToolcalls_UsesLastSection(t *testing.T) {
	reasoning := strings.Join([]string{
		"<|tool_calls_section_begin|>",
		"<|tool_call_begin|> functions.Bash:0 <|tool_call_argument_begin|> {\"command\": \"old\"} <|tool_call_end|>",
		"<|tool_calls_section_end|>",
		"wait, let me reconsider",
		"<|tool_calls_section_begin|>",
		"<|tool_call_begin|> functions.Read:0 <|tool_call_argument_begin|> {\"path\": \"new.go\"} <|tool_call_end|>",
		"<|tool_calls_section_end|>",
	}, "\n")

	calls := parseThinkingToolcalls(reasoning)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call from last section, got %d", len(calls))
	}
	if calls[0].Name != "Read" {
		t.Fatalf("expected tool from last section (Read), got %q", calls[0].Name)
	}
}

func TestParseThinkingToolcalls_FunctionWithoutIndex(t *testing.T) {
	reasoning := `<|tool_calls_section_begin|> <|tool_call_begin|> functions.Bash: <|tool_call_argument_begin|> {"command": "ls"} <|tool_call_end|> <|tool_calls_section_end|>`

	calls := parseThinkingToolcalls(reasoning)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "Bash" {
		t.Fatalf("expected tool name Bash, got %q", calls[0].Name)
	}
	if calls[0].ID != " functions.Bash:" {
		t.Fatalf("expected ID ' functions.Bash:', got %q", calls[0].ID)
	}
}

func TestParseThinkingToolcalls_NonStdJSONEscapes(t *testing.T) {
	// Real case from logs: model emits \xHH in grep pattern args.
	reasoning := " ```\n" +
		` <|tool_calls_section_begin|> <|tool_call_begin|>  functions.Bash:15 <|tool_call_argument_begin|> {"command": "git diff --staged | grep -A5 -B5 '\xe8\xa7\xa3' | head -100", "description": "check unicode encoding"} <|tool_call_end|> <|tool_calls_section_end|>`

	calls := parseThinkingToolcalls(reasoning)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "Bash" {
		t.Fatalf("expected tool name Bash, got %q", calls[0].Name)
	}
	// The \xHH should have been normalised to \u00HH in the args JSON.
	if !strings.Contains(string(calls[0].Args), `\u00e8\u00a7\u00a3`) {
		t.Fatalf("expected args to contain normalised unicode escapes, got %s", calls[0].Args)
	}
}

func TestFixNonStdJSONEscapes(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{`\xe8\xa7\xa3`, `\u00e8\u00a7\u00a3`},
		{`\xAB\xCD`, `\u00AB\u00CD`},
		{`no escapes here`, `no escapes here`},
		{`mixed \xe8 and normal`, `mixed \u00e8 and normal`},
	}
	for _, tt := range tests {
		got := fixNonStdJSONEscapes(tt.in)
		if got != tt.want {
			t.Errorf("fixNonStdJSONEscapes(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
