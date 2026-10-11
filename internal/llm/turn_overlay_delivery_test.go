package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

// A request-scoped user overlay is how the runtime appends state that must
// reach the model but is not prose the user typed: turn reminders, the file
// snapshot replay, and the question projection that re-states archived
// questions and decisions after a compaction. Every wire has to deliver it.
// Dropping it silently is the worst failure mode available, because the
// producing side (and its unit tests) still look correct while the model never
// sees the row.
//
// The producers mark such rows KindTurnOverlay rather than sending a plain
// user message so the row does not become a new turn boundary. The transient
// flag asserted below is what keeps that promise on the OpenAI chat wire:
// fillMissingReasoning and ensureChatGeminiActiveLoopSignatures both scan back
// to the last non-transient user row, so a non-transient overlay at the request
// tail would push the current-turn window past the turn's own assistant tool
// calls and make those endpoints reject the request.
func TestRequestScopedOverlaysReachEveryWire(t *testing.T) {
	const text = "OVERLAY-PROBE"
	const decisionIDs = "Question ID: question-1\nResult ID: result-1"
	history := func() []message.Message {
		return []message.Message{
			{Role: message.RoleUser, Content: "hello"},
			{Role: message.RoleAssistant, Content: "hi"},
			{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: text + "\n" + decisionIDs},
			{Role: message.RoleUser, Content: "tail"},
		}
	}

	chatRows := convertMessagesToOpenAIWithOptions("sys", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, history(), openAIConvertOptions{})
	var overlayRow *openAIMessage
	for i := range chatRows {
		if chatRows[i].Role == "user" && strings.Contains(stringifyContent(chatRows[i].Content), text) {
			overlayRow = &chatRows[i]
			break
		}
	}
	if overlayRow == nil {
		t.Fatalf("openai chat dropped the overlay: %#v", chatRows)
	}
	if !overlayRow.Transient {
		t.Fatal("openai chat delivered the overlay as a real user turn, which moves the reasoning-replay boundary")
	}
	for _, want := range []string{"Question ID: question-1", "Result ID: result-1"} {
		if !strings.Contains(stringifyContent(overlayRow.Content), want) {
			t.Fatalf("openai chat dropped decision identifier %q: %#v", want, overlayRow.Content)
		}
	}

	for _, tc := range []struct {
		wire   string
		render func() string
	}{
		{"anthropic", func() string {
			out, _ := convertMessagesWithMap(history())
			b, _ := json.Marshal(out)
			return string(b)
		}},
		{"gemini", func() string {
			b, _ := json.Marshal(convertMessagesToGemini(history()))
			return string(b)
		}},
		{"responses", func() string {
			b, _ := json.Marshal(convertMessagesToResponsesWithItemIDs("sys", history(), false, false, false))
			return string(b)
		}},
	} {
		t.Run(tc.wire, func(t *testing.T) {
			wire := tc.render()
			if !strings.Contains(wire, text) {
				t.Fatalf("wire dropped the overlay: %s", wire)
			}
			for _, want := range []string{"Question ID: question-1", "Result ID: result-1"} {
				if !strings.Contains(wire, want) {
					t.Fatalf("wire dropped decision identifier %q: %s", want, wire)
				}
			}
		})
	}
}

func stringifyContent(content any) string {
	if content == nil {
		return ""
	}
	b, err := json.Marshal(content)
	if err != nil {
		return ""
	}
	return string(b)
}

// The overlay has to keep its identity after conversion on the native Gemini
// wire too. geminiContent carries no message kind, so the distinction rides the
// part; ensureGeminiActiveLoopSignatures keys the active loop on the last user
// content that carries text, and an overlay counted as that boundary starts the
// loop after the current turn's function calls — leaving them without the
// Gemini 3 thought-signature repair, silently.
func TestTrailingOverlayKeepsTheGeminiActiveLoopRepair(t *testing.T) {
	history := func(tail ...message.Message) []message.Message {
		msgs := []message.Message{
			{Role: message.RoleUser, Content: "do the thing"},
			{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}}},
			{Role: message.RoleTool, ToolCallID: "call-1", Content: "result"},
		}
		return append(msgs, tail...)
	}
	// repaired reports whether the current turn's function call received the
	// placeholder signature the Gemini 3 contract requires.
	repaired := func(msgs []message.Message) bool {
		contents := convertMessagesToGemini(msgs)
		ensureGeminiActiveLoopSignatures(contents)
		for _, content := range contents {
			for _, part := range content.Parts {
				if part.FunctionCall != nil && part.ThoughtSignature != "" {
					return true
				}
			}
		}
		return false
	}

	if !repaired(history()) {
		t.Fatal("the current turn's function call was left unrepaired")
	}
	if !repaired(history(message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "<system-reminder>\nCurrent questions: [...]\n</system-reminder>"})) {
		t.Fatal("a trailing overlay disabled the Gemini thought-signature repair")
	}
	// A real user message still moves the boundary — the loop starts after it,
	// so the earlier turn's function call is not repaired again. Without this
	// control the assertion above would also pass if the scan ignored every
	// user content.
	if repaired(history(message.Message{Role: message.RoleUser, Content: "a real follow-up"})) {
		t.Fatal("a real user message stopped moving the active-loop boundary")
	}
}
