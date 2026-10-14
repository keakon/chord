package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/session"
)

const summaryReasoningSentinel = "INDEPENDENT_REASONING_SENTINEL"

func summaryProjectionFixture() message.Message {
	secret := strings.Repeat(summaryReasoningSentinel, 4096)
	return message.Message{
		Role: message.RoleAssistant, Content: "Visible analysis <think>literal</think> thinking rationale.",
		ThinkingBlocks:   []message.ThinkingBlock{{Thinking: secret, Signature: secret}, {Data: secret}},
		ReasoningContent: secret,
		ResponsesOutput: []message.ResponsesOutputItem{
			{Type: "reasoning", ID: "r1", Summary: []message.ResponsesReasoningSummary{{Text: secret}}, EncryptedContent: secret},
			{Type: "message", ID: "m1", Role: "assistant", Phase: "final_answer", Content: []message.ResponsesOutputContent{{Type: "output_text", Text: "stale replay text"}}, Summary: []message.ResponsesReasoningSummary{{Text: secret}}, EncryptedContent: secret},
			{Type: "function_call", CallID: "call-1", Name: "read", Arguments: `{"path":"old.go"}`},
		},
		GeminiParts: []message.GeminiReplayPart{{Type: "thought", Text: secret, ThoughtSignature: secret}, {Type: "text", Text: "stale replay text", ThoughtSignature: secret}, {Type: "function_call", ToolCallID: "call-1", ThoughtSignature: secret}},
		ToolCalls:   []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"new.go"}`), ThoughtSignature: secret}},
		NativeTools: &message.NativeToolHistory{Items: []json.RawMessage{json.RawMessage(`{"type":"text","text":"native visible fact"}`)}},
		ToolStatus:  message.ToolStatusError, ToolRecoveryState: message.ToolRecoveryStateOutcomeUnknown,
		ToolCallID: "result-id", ToolDiff: "diff", Audit: &message.ToolArgsAudit{OriginalArgsJSON: `{"path":"old.go"}`},
		Provenance: &message.MessageProvenance{Origin: message.OriginUser}, Kind: "fixture",
		CompactionFileRevisions: map[string]string{"new.go": "revision"},
	}
}

func TestProjectMessagesForCompactionSummaryStripsReasoning(t *testing.T) {
	for _, role := range []message.Role{message.RoleAssistant, message.RoleUser, message.RoleTool, message.RoleSystem} {
		t.Run(string(role), func(t *testing.T) {
			source := summaryProjectionFixture()
			source.Role = role
			// Spare capacity detects accidental in-place filtering of the source.
			source.ResponsesOutput = append(source.ResponsesOutput, message.ResponsesOutputItem{Type: "spare"})[:3]
			source.GeminiParts = append(source.GeminiParts, message.GeminiReplayPart{Type: "spare"})[:3]
			snapshot := source.Clone()
			got, err := projectMessagesForCompactionSummary([]message.Message{source})
			if err != nil {
				t.Fatal(err)
			}
			want := source.Clone()
			want.ThinkingBlocks = nil
			want.ReasoningContent = ""
			want.ResponsesOutput = want.ResponsesOutput[1:]
			for i := range want.ResponsesOutput {
				want.ResponsesOutput[i].Summary = nil
				want.ResponsesOutput[i].EncryptedContent = ""
			}
			want.GeminiParts = want.GeminiParts[1:]
			for i := range want.GeminiParts {
				want.GeminiParts[i].ThoughtSignature = ""
			}
			want.ToolCalls[0].ThoughtSignature = ""
			if !reflect.DeepEqual(got[0], want) {
				t.Fatal("projection changed retained fields or retained reasoning")
			}
			if !reflect.DeepEqual(source, snapshot) {
				t.Fatal("projection mutated source")
			}
			again, err := projectMessagesForCompactionSummary(got)
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatal("projection is not idempotent", err)
			}
			exported, err := session.Export(normalizeMessagesForSummary(got), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			markdown := session.ExportToMarkdown(exported)
			if strings.Contains(markdown, summaryReasoningSentinel) || strings.Contains(markdown, "stale replay text") {
				t.Fatal("replay leaked into Markdown")
			}
			if role == message.RoleAssistant && (strings.Count(markdown, source.Content) != 1 || !strings.Contains(markdown, "new.go")) {
				t.Fatal("canonical data lost or duplicated")
			}
		})
	}
}

func TestProjectMessagesForCompactionSummaryClearsReplaySignatures(t *testing.T) {
	source := summaryProjectionFixture()
	source.ThinkingBlocks = nil
	source.ReasoningContent = ""
	source.ResponsesOutput = source.ResponsesOutput[1:]
	source.GeminiParts = source.GeminiParts[1:]
	source.ToolCalls = append(source.ToolCalls, message.ToolCall{ID: "call-2", Name: "read", Args: json.RawMessage(`{}`), ThoughtSignature: summaryReasoningSentinel})
	got, err := projectMessagesForCompactionSummary([]message.Message{source})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), summaryReasoningSentinel) {
		t.Fatal("signature retained without thought text")
	}
	if got[0].ToolCalls[1].ID != "call-2" {
		t.Fatal("call order changed")
	}
}

func TestProjectMessagesForCompactionSummaryRejectsMissingCanonicalData(t *testing.T) {
	cases := []struct {
		name     string
		msg      message.Message
		category string
	}{
		{"responses text", message.Message{ResponsesOutput: []message.ResponsesOutputItem{{Type: "message", Content: []message.ResponsesOutputContent{{Text: "private body"}}}}}, "visible replay text without canonical text"},
		{"refusal", message.Message{Content: " \n", ResponsesOutput: []message.ResponsesOutputItem{{Type: "message", Content: []message.ResponsesOutputContent{{Refusal: "private refusal"}}}}}, "visible replay text without canonical text"},
		{"gemini text", message.Message{GeminiParts: []message.GeminiReplayPart{{Type: "text", Text: "private body"}}}, "visible replay text without canonical text"},
		{"responses call", message.Message{ResponsesOutput: []message.ResponsesOutputItem{{Type: "function_call", CallID: "absent"}}}, "replay call without canonical tool call"},
		{"gemini call", message.Message{GeminiParts: []message.GeminiReplayPart{{Type: "function_call", ToolCallID: "absent"}}}, "replay call without canonical tool call"},
		{"empty responses id", message.Message{ToolCalls: []message.ToolCall{{ID: ""}}, ResponsesOutput: []message.ResponsesOutputItem{{Type: "function_call"}}}, "replay call without canonical tool call"},
		{"empty gemini id", message.Message{ToolCalls: []message.ToolCall{{ID: ""}}, GeminiParts: []message.GeminiReplayPart{{Type: "function_call"}}}, "replay call without canonical tool call"},
		{"exact id", message.Message{ToolCalls: []message.ToolCall{{ID: "call"}}, GeminiParts: []message.GeminiReplayPart{{Type: "function_call", ToolCallID: " call "}}}, "replay call without canonical tool call"},
		{"text part", message.Message{Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "canonical"}}, GeminiParts: []message.GeminiReplayPart{{Type: "text", Text: "replay"}}}, ""},
		{"blank replay", message.Message{GeminiParts: []message.GeminiReplayPart{{Type: "text", Text: " \n"}}}, ""},
		{"thought only", message.Message{GeminiParts: []message.GeminiReplayPart{{Type: "thought", Text: "secret"}}, ResponsesOutput: []message.ResponsesOutputItem{{Type: "reasoning", Summary: []message.ResponsesReasoningSummary{{Text: "secret"}}}}}, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.msg.Role = message.RoleAssistant
			_, err := projectMessagesForCompactionSummary([]message.Message{{Role: message.RoleUser}, tt.msg})
			if tt.category == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			want := "compaction summary projection: message 1 has " + tt.category
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %s", err, want)
			}
		})
	}
}

func TestProjectMessagesForCompactionSummaryIsIdempotent(t *testing.T) {
	for _, source := range [][]message.Message{nil, {}, {{Role: message.RoleAssistant}, {Role: message.RoleAssistant, ThinkingBlocks: []message.ThinkingBlock{{Thinking: "secret"}}}, {Role: message.RoleUser, Content: "last"}}} {
		got, err := projectMessagesForCompactionSummary(source)
		if err != nil || len(got) != len(source) || (source == nil) != (got == nil) {
			t.Fatal("shape changed", err)
		}
		twice, err := projectMessagesForCompactionSummary(got)
		if err != nil || !reflect.DeepEqual(got, twice) {
			t.Fatal("not idempotent", err)
		}
		for i := range source {
			if source[i].Role != got[i].Role || source[i].Content != got[i].Content {
				t.Fatal("message order changed")
			}
		}
	}
}

func TestCompactionSummaryReasoningDoesNotConsumeTranscriptBudget(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	rich := []message.Message{{Role: message.RoleUser, Content: "Keep the visible requirement."}, summaryProjectionFixture(), {Role: message.RoleTool, ToolCallID: "call-1", Content: "visible execution result"}}
	rich[1].NativeTools = nil // Native history has its own authoritative cost surface.
	plain, err := projectMessagesForCompactionSummary(rich)
	if err != nil {
		t.Fatal(err)
	}
	const limit = 16384
	build := func(head []message.Message) *compactionInput {
		t.Helper()
		got, err := a.buildCompactionInputWithOptions(head, limit, nil, nil, compactionAnchors{})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	baseline, projected := build(plain), build(rich)
	if !reflect.DeepEqual(baseline, projected) {
		t.Fatal("independent reasoning changed summary input")
	}
	_, pm, err := a.prepareCompactionSummaryMessages(plain)
	if err != nil {
		t.Fatal(err)
	}
	_, rm, err := a.prepareCompactionSummaryMessages(rich)
	if err != nil {
		t.Fatal(err)
	}
	if estimateMessagesTokens(a.ctxMgr, pm) != estimateMessagesTokens(a.ctxMgr, rm) {
		t.Fatal("projected estimate changed")
	}
	oldPruned := a.compactionReductionScratch().prepareMessagesForLLM(rich)
	oldTrimmed, oldOmitted := trimMessagesToBudget(a.ctxMgr, normalizeMessagesForSummary(oldPruned), compactionInputBudget(limit))
	if oldOmitted <= projected.OmittedMessages {
		t.Fatal("fixture does not expose old reasoning budget cost")
	}
	oldExport, err := session.Export(oldTrimmed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldInput := *projected
	oldInput.Transcript = session.ExportToMarkdown(oldExport)
	oldInput.OmittedMessages = oldOmitted
	for _, row := range []struct {
		name  string
		input *compactionInput
	}{{"before", &oldInput}, {"after", projected}} {
		prompt := buildCompactionPromptWithKeyFiles(row.input, "history-1.md", nil, nil, nil, nil)
		t.Logf("offline %s: prompt_bytes=%d estimated_tokens=%d omitted=%d requirement=%t reply=%t execution=%t", row.name, len(prompt), compactionPromptTokenEstimate(row.input, "history-1.md", nil, nil, nil, nil), row.input.OmittedMessages, strings.Contains(row.input.Transcript, "Keep the visible requirement."), strings.Contains(row.input.Transcript, rich[1].Content), strings.Contains(row.input.Transcript, "visible execution result"))
	}
	if buildCompactionPromptWithKeyFiles(baseline, "history-1.md", nil, nil, nil, nil) != buildCompactionPromptWithKeyFiles(projected, "history-1.md", nil, nil, nil, nil) {
		t.Fatal("final prompt changed")
	}
}

func TestCompactionSummaryFitUsesProjectedMessages(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	head := []message.Message{{Role: message.RoleUser, Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "Inspect the attachment."}, {Type: message.ContentPartImage, Data: []byte{1, 2, 3}}}}, summaryProjectionFixture(), {Role: message.RoleTool, ToolCallID: "call-1", ToolStatus: message.ToolStatusCancelled, ToolRecoveryState: message.ToolRecoveryStateOutcomeUnknown, Content: "visible outcome unknown"}}
	snapshots := make([]message.Message, len(head))
	for i := range head {
		snapshots[i] = head[i].Clone()
	}
	input, err := a.buildCompactionInputWithOptions(head, 16384, nil, head[1:2], compactionAnchors{})
	if err != nil {
		t.Fatal(err)
	}
	background := []recovery.BackgroundObjectState{{ID: "huge", Description: strings.Repeat("auxiliary state ", 10000)}}
	got, inputs, err := a.fitCompactionInputToContextLimit(head, input, 16384, "history-1.md", nil, nil, nil, background, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if got == input || len(inputs.BackgroundObjects) != 0 {
		t.Fatal("fixture did not force fit rebuild and degradation")
	}
	prompt := buildCompactionPromptWithKeyFiles(got, "history-1.md", inputs.KeyFiles, inputs.Todos, inputs.SubAgents, inputs.BackgroundObjects)
	if strings.Contains(prompt, summaryReasoningSentinel) {
		t.Fatal("reasoning leaked during fit")
	}
	if compactionPromptTokenEstimate(got, "history-1.md", inputs.KeyFiles, inputs.Todos, inputs.SubAgents, inputs.BackgroundObjects) > 16384-2048-max(16384/compactPreflightBufferRatio, compactPreflightBufferMin) {
		t.Fatal("prompt exceeds admission budget")
	}
	for _, want := range []string{"image attachment", "new.go", "visible outcome unknown"} {
		if !strings.Contains(got.Transcript, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if !reflect.DeepEqual(head, snapshots) || !reflect.DeepEqual(got.RecentTail, head[1:2]) {
		t.Fatal("build/fit changed source or raw tail")
	}
	bad := []message.Message{{Role: message.RoleAssistant, GeminiParts: []message.GeminiReplayPart{{Type: "text", Text: "missing canonical"}}}}
	_, _, err = a.fitCompactionInputToContextLimit(bad, input, 16384, "history-1.md", nil, nil, nil, background, 2048)
	if err == nil || !strings.Contains(err.Error(), "prepare compaction transcript during fit: compaction summary projection") {
		t.Fatalf("fit did not propagate projection error: %v", err)
	}
}

// Reuse the existing capture provider's transport implementation; only the
// response sequence differs so the real format-repair request is observable.
type summaryProjectionCaptureProvider struct {
	compactionPromptCaptureProvider
	prompts []string
}

func (p *summaryProjectionCaptureProvider) CompleteStream(ctx context.Context, apiKey, apiURL, model string, messages []message.Message, definitions []message.ToolDefinition, maxTokens int, tuning llm.RequestTuning, callback llm.StreamCallback) (*message.Response, error) {
	response, err := p.compactionPromptCaptureProvider.CompleteStream(ctx, apiKey, apiURL, model, messages, definitions, maxTokens, tuning, callback)
	p.prompts = append(p.prompts, p.prompt)
	if len(p.prompts) == 1 {
		response.Content = "too short"
	}
	return response, err
}
func summaryProjectionClient(t *testing.T, a *MainAgent) *summaryProjectionCaptureProvider {
	t.Helper()
	a.SetProviderModelRef("sample/compact-model")
	cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, Models: map[string]config.ModelConfig{"compact-model": {Limit: config.ModelLimit{Context: 16384, Output: 2048}}}}, []string{"test-key"})
	provider := &summaryProjectionCaptureProvider{}
	a.llmClient = llm.NewClient(cfg, provider, "compact-model", 2048, "")
	return provider
}
func TestCompactionSummaryRequestContainsNoIndependentReasoning(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	provider := summaryProjectionClient(t, a)
	head := []message.Message{{Role: message.RoleUser, Content: "Continue the visible task."}, summaryProjectionFixture(), {Role: message.RoleTool, ToolCallID: "call-1", Content: "visible execution result"}}
	_, _, err := summarizeCompactionHeadForTest(a, head, "history-1.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.prompts) != 2 {
		t.Fatalf("requests=%d, want initial and repair", len(provider.prompts))
	}
	for _, prompt := range provider.prompts {
		if strings.Contains(prompt, summaryReasoningSentinel) || strings.Contains(prompt, "stale replay text") {
			t.Fatal("replay leaked into request")
		}
		if !strings.Contains(prompt, head[1].Content) || !strings.Contains(prompt, "visible execution result") {
			t.Fatal("visible facts missing in request")
		}
	}
}

func TestCompactionSummaryKeepsRawTailAndArchive(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	head := []message.Message{{Role: message.RoleUser, Content: "Continue."}, summaryProjectionFixture()}
	tail := []message.Message{summaryProjectionFixture()}
	tail[0].NativeTools = nil
	input, err := a.buildCompactionInputWithOptions(head, 16384, nil, tail, compactionAnchors{})
	if err != nil {
		t.Fatal(err)
	}
	path, _, _, err := a.exportCompactionHistory(head, head, 1, nil, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(archive), summaryReasoningSentinel) {
		t.Fatal("archive lost original reasoning")
	}
	if !reflect.DeepEqual(input.RecentTail, tail) || strings.Contains(input.RecentTailAnchor, summaryReasoningSentinel) {
		t.Fatal("raw tail changed or preview leaked reasoning")
	}
	plain, err := projectMessagesForCompactionSummary(tail)
	if err != nil {
		t.Fatal(err)
	}
	if estimateMessagesTokens(a.ctxMgr, tail) <= estimateMessagesTokens(a.ctxMgr, plain) {
		t.Fatal("raw replay cost no longer includes reasoning")
	}
}

func TestCompactionSummaryProjectionFailurePreservesHistory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		msg  message.Message
	}{
		{"responses text", message.Message{ResponsesOutput: []message.ResponsesOutputItem{{Type: "message", Content: []message.ResponsesOutputContent{{Text: "Visible replay fact."}}}}}},
		{"responses refusal", message.Message{ResponsesOutput: []message.ResponsesOutputItem{{Type: "message", Content: []message.ResponsesOutputContent{{Refusal: "Visible refusal."}}}}}},
		{"gemini text", message.Message{GeminiParts: []message.GeminiReplayPart{{Type: "text", Text: "Visible replay fact."}}}},
		{"responses call", message.Message{ResponsesOutput: []message.ResponsesOutputItem{{Type: "function_call", CallID: "call-1", Name: "read", Arguments: `{"path":"sample.go"}`}}}},
		{"gemini call", message.Message{GeminiParts: []message.GeminiReplayPart{{Type: "function_call", ToolCallID: "call-1"}}}},
	}
	for _, tc := range cases {
		for _, unavailable := range []bool{false, true} {
			name := tc.name
			if unavailable {
				name += "/client unavailable"
			}
			t.Run(name, func(t *testing.T) {
				a := newTestMainAgent(t, t.TempDir())
				provider := summaryProjectionClient(t, a)
				if unavailable {
					a.llmClient = nil
				}
				msg := tc.msg.Clone()
				msg.Role = message.RoleAssistant
				msg.ThinkingBlocks = []message.ThinkingBlock{{Thinking: summaryReasoningSentinel}}
				snapshot := []message.Message{{Role: message.RoleUser, Content: "Continue the visible task."}, msg, {Role: message.RoleUser, Content: "Keep the API stable."}, {Role: message.RoleAssistant, Content: "Visible progress."}}
				var transcript []byte
				for _, m := range snapshot {
					a.ctxMgr.Append(m)
					line, err := json.Marshal(m)
					if err != nil {
						t.Fatal(err)
					}
					transcript = append(transcript, line...)
					transcript = append(transcript, '\n')
				}
				mainPath := filepath.Join(a.sessionDir, "main.jsonl")
				if err := os.WriteFile(mainPath, transcript, 0600); err != nil {
					t.Fatal(err)
				}
				before := a.ctxMgr.Snapshot()
				draft, err := a.produceCompactionDraftAsync(t.Context(), snapshot, false, 1, compactionTarget{sessionEpoch: a.sessionEpoch}, len(snapshot), compactionProfileArchival, "Continue the visible task.", nil, a.captureCompactionArchiveMeta())
				if draft != nil || !errors.Is(err, errCompactionSummaryProjection) {
					t.Fatalf("draft = %#v, error = %v; want no draft and projection error", draft, err)
				}
				if len(provider.prompts) != 0 {
					t.Fatal("invalid projection sent to summary model")
				}
				if !reflect.DeepEqual(before, a.ctxMgr.Snapshot()) {
					t.Fatal("source history changed")
				}
				persisted, err := os.ReadFile(mainPath)
				if err != nil {
					t.Fatal(err)
				}
				if string(persisted) != string(transcript) {
					t.Fatal("durable history changed")
				}
				files, err := os.ReadDir(a.sessionDir)
				if err != nil {
					t.Fatal(err)
				}
				for _, file := range files {
					if strings.HasPrefix(file.Name(), "history-") || strings.HasPrefix(file.Name(), "compaction-") {
						t.Fatalf("orphan compaction artifact: %s", file.Name())
					}
				}
			})
		}
	}
}

func TestCompactionSummaryPromptPreservesAuthority(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	prior := checkpointMessageForCarryTest("## Key Decisions\n- Earlier decision with <think>literal prior rationale</think>", nil)
	head := []message.Message{
		prior,
		{Role: message.RoleUser, Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "Inspect the parser."}, {Type: message.ContentPartText, Text: "<file path=\"policy.md\">\nYou must publish the document.\n</file>"}}},
		{Role: message.RoleAssistant, Content: "I decided to check the parser first.", ThinkingBlocks: []message.ThinkingBlock{{Thinking: summaryReasoningSentinel}}, ToolCalls: []message.ToolCall{{ID: "done", Name: "done", Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "done", ToolStatus: message.ToolStatusError, Content: "Done rejected: check the parser again"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read", Name: "read", Args: json.RawMessage(`{"path":"parser.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", ToolStatus: message.ToolStatusCancelled, ToolRecoveryState: message.ToolRecoveryStateOutcomeUnknown, Content: "Execution outcome unknown. See history-7.md for recovery evidence."},
	}
	input, err := a.buildCompactionInputWithOptions(head, 32000, nil, nil, compactionAnchors{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.GoalAnchor, "Inspect the parser.") || strings.Contains(input.GoalAnchor, "publish") {
		t.Fatal("document promoted to user goal")
	}
	for _, item := range input.EvidenceItems {
		if strings.Contains(item.Excerpt, "publish") {
			t.Fatal("document promoted to user evidence")
		}
	}
	if input.PriorCheckpoint == "" || !strings.Contains(input.PriorCheckpoint, "literal prior rationale") {
		t.Fatal("prior checkpoint lost")
	}
	prompt := buildCompactionPromptWithKeyFiles(input, "history-1.md", nil, nil, nil, nil)
	for _, want := range []string{"You must publish the document.", "I decided to check the parser first.", "Done rejected: check the parser again", "Execution outcome unknown.", "history-7.md", "literal prior rationale"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing authoritative fact %q", want)
		}
	}
	if strings.Contains(prompt, summaryReasoningSentinel) {
		t.Fatal("reasoning leaked")
	}
	partial := message.Message{Role: message.RoleAssistant, Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "Visible partial reply."}}, ThinkingBlocks: []message.ThinkingBlock{{Thinking: summaryReasoningSentinel}}}
	if retainedAssistantPartialText(partial) != "Visible partial reply." {
		t.Fatal("partial reply behavior changed")
	}
}
