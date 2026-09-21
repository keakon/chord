package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const testResultContractSchema = `{"type":"object","required":["name","count"],"properties":{"name":{"type":"string"},"count":{"type":"integer"},"mode":{"type":"string","enum":["fast","slow"]}}}`

func setTestResultContract(t *testing.T, sub *SubAgent, raw string) json.RawMessage {
	t.Helper()
	schema, canonical, err := tools.CompileResultSchema(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("CompileResultSchema(%s): %v", raw, err)
	}
	sub.resultSchema = schema
	sub.resultSchemaJSON = canonical
	return canonical
}

// installContractFollowUpLLM scripts one provider call so a rejected Complete's
// follow-up request has a provider to reach, mirroring the invalid-Complete
// tests (the forced tool-choice tuning requires a tool call).
func installContractFollowUpLLM(t *testing.T, sub *SubAgent) *blockingStreamProvider {
	t.Helper()
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "retry-1", "complete", map[string]any{"summary": "corrected"})})},
	}}}
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")
	return provider
}

func toolResultForCall(t *testing.T, sub *SubAgent, callID string) message.Message {
	t.Helper()
	for _, msg := range sub.ctxMgr.Snapshot() {
		if msg.Role == "tool" && msg.ToolCallID == callID {
			return msg
		}
	}
	t.Fatalf("no tool result for call %q", callID)
	return message.Message{}
}

func TestResultContractViolationGetsBoundedRepair(t *testing.T) {
	tests := []struct {
		name     string
		result   map[string]any
		wantPath string
	}{
		{name: "missing required field", result: map[string]any{"count": 2}, wantPath: "result.name is required"},
		{name: "wrong field type", result: map[string]any{"name": "x", "count": "two"}, wantPath: "result.count must be an integer"},
		{name: "enum mismatch", result: map[string]any{"name": "x", "count": 1, "mode": "medium"}, wantPath: "result.mode must be one of"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent, sub := newMixedBatchTestSubAgent(t)
			setTestResultContract(t, sub, testResultContractSchema)
			provider := installContractFollowUpLLM(t, sub)

			sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "done", "result_type": "review_report", "result": tc.result}),
			})}})

			// A contract violation is repairable: no terminal event, one bounded
			// follow-up, and neither of the other two budgets is touched.
			select {
			case evt := <-parent.eventCh:
				t.Fatalf("result contract violation terminated the task with %#v instead of a bounded follow-up", evt)
			default:
			}
			if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
				t.Fatalf("follow-up request failed: %v", result.err)
			}
			if got := sub.turn.SubAgentResultContractRecoveryCount; got != 1 {
				t.Fatalf("contract recovery count = %d, want 1", got)
			}
			if got := sub.turn.SubAgentCompletionRecoveryCount; got != 0 {
				t.Fatalf("argument recovery count = %d, want 0 (the budgets are independent)", got)
			}
			if got := sub.turn.SubAgentTerminalRecoveryCount; got != 0 {
				t.Fatalf("terminal recovery count = %d, want 0", got)
			}
			rejected := toolResultForCall(t, sub, "call-1")
			if !strings.HasPrefix(rejected.Content, "Completion rejected:") || !strings.Contains(rejected.Content, tc.wantPath) {
				t.Fatalf("rejection content = %q, want it to name %q", rejected.Content, tc.wantPath)
			}
			if rejected.ToolStatus != string(ToolResultStatusError) {
				t.Fatalf("rejected Complete ToolStatus = %q, want %q", rejected.ToolStatus, ToolResultStatusError)
			}
			assertCompleteCardStatus(t, parent, sub, "call-1", ToolResultStatusError)
			seen, _ := provider.snapshot()
			if len(seen) != 1 {
				t.Fatalf("provider request count = %d, want 1 bounded follow-up", len(seen))
			}
			tail := seen[0][len(seen[0])-1]
			if tail.Role != "user" || !strings.Contains(tail.Content, "result contract") {
				t.Fatalf("follow-up tail = %#v, want the contract repair nudge", tail)
			}
			if sub.resultContractFailure() != nil {
				t.Fatal("a repairable violation must not be recorded as the terminal contract failure")
			}

			// The corrected delivery is accepted within the same turn.
			sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "call-2", "complete", map[string]any{"summary": "done", "result_type": "review_report", "result": map[string]any{"name": "x", "count": 1}}),
			})}})
			select {
			case evt := <-parent.eventCh:
				if evt.Type != EventAgentDone {
					t.Fatalf("corrected completion event = %#v, want EventAgentDone", evt)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for the corrected completion")
			}
		})
	}
}

func TestResultContractSummaryOnlyGetsRepair(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	setTestResultContract(t, sub, testResultContractSchema)
	installContractFollowUpLLM(t, sub)

	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "done"}),
	})}})

	select {
	case evt := <-parent.eventCh:
		t.Fatalf("a summary without a result terminated the task with %#v instead of a bounded follow-up", evt)
	default:
	}
	if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
		t.Fatalf("follow-up request failed: %v", result.err)
	}
	if got := sub.turn.SubAgentResultContractRecoveryCount; got != 1 {
		t.Fatalf("contract recovery count = %d, want 1", got)
	}
	if rejection := toolResultForCall(t, sub, "call-1"); !strings.Contains(rejection.Content, "no machine-readable result") {
		t.Fatalf("rejection content = %q, want the missing-result diagnosis", rejection.Content)
	}
}

func TestResultContractMissingGroupIsViolationNotDegraded(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	setTestResultContract(t, sub, testResultContractSchema)
	installContractFollowUpLLM(t, sub)

	// A result without result_type is the typedResultPairingError class that is
	// normally repaired by dropping the group. With a declared contract the group
	// is the deliverable, so the same shape is a missing result.
	missingGroup := map[string]any{"summary": "done", "result": map[string]any{"name": "x", "count": 1}}
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", missingGroup),
	})}})

	select {
	case evt := <-parent.eventCh:
		t.Fatalf("missing typed group terminated the task with %#v instead of using the contract repair budget", evt)
	default:
	}
	if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
		t.Fatalf("follow-up request failed: %v", result.err)
	}
	if got := sub.turn.SubAgentResultContractRecoveryCount; got != 1 {
		t.Fatalf("contract recovery count = %d, want 1", got)
	}
	if got := sub.turn.SubAgentCompletionRecoveryCount; got != 0 {
		t.Fatalf("argument recovery count = %d, want 0 (a missing group is a contract violation, not an argument error)", got)
	}
	if rejection := toolResultForCall(t, sub, "call-1"); !strings.Contains(rejection.Content, "no machine-readable result") {
		t.Fatalf("rejection content = %q, want the missing-result diagnosis", rejection.Content)
	}

	// Budget spent: the violation is terminal and there is no degraded success.
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-2", "complete", missingGroup),
	})}})
	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError {
			t.Fatalf("event.Type = %q, want %q (a declared contract must never degrade to success)", evt.Type, EventAgentError)
		}
		err, ok := evt.Payload.(error)
		if !ok {
			t.Fatalf("payload = %#v, want an error", evt.Payload)
		}
		violation, ok := errors.AsType[*ResultContractViolationError](err)
		if !ok {
			t.Fatalf("payload = %T, want *ResultContractViolationError", err)
		}
		if got := violation.Diagnostics().Category; got != resultContractCategoryMissingResult {
			t.Fatalf("diagnostics category = %q, want %q", got, resultContractCategoryMissingResult)
		}
		if got := classifyAgentError(err); got != agentErrorKindContract {
			t.Fatalf("classifyAgentError = %q, want %q", got, agentErrorKindContract)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the terminal contract violation")
	}
	assertCompleteCardStatus(t, parent, sub, "call-2", ToolResultStatusError)
	failure := sub.resultContractFailure()
	if failure == nil || failure.Category != resultContractCategoryMissingResult {
		t.Fatalf("recorded contract failure = %#v, want the missing-result diagnostics", failure)
	}
}

func TestResultContractBudgetIsIndependentOfArgumentBudget(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	setTestResultContract(t, sub, testResultContractSchema)
	installContractFollowUpLLM(t, sub)
	// The invalid-argument budget is already spent.
	sub.turn.SubAgentCompletionRecoveryCount = 1

	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "done", "result_type": "review_report", "result": map[string]any{"name": "x", "count": "two"}}),
	})}})
	select {
	case evt := <-parent.eventCh:
		t.Fatalf("a spent argument budget terminated a contract violation: %#v", evt)
	default:
	}
	if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
		t.Fatalf("follow-up request failed: %v", result.err)
	}
	if got := sub.turn.SubAgentResultContractRecoveryCount; got != 1 {
		t.Fatalf("contract recovery count = %d, want 1 despite the spent argument budget", got)
	}

	// With the contract budget now spent too, the next violation is terminal.
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-2", "complete", map[string]any{"summary": "done", "result_type": "review_report", "result": map[string]any{"name": "x", "count": "two"}}),
	})}})
	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError {
			t.Fatalf("event.Type = %q, want %q", evt.Type, EventAgentError)
		}
	default:
		t.Fatal("contract violation must be terminal once both budgets are spent")
	}
	failure := sub.resultContractFailure()
	if failure == nil || len(failure.Violations) == 0 || failure.Violations[0].Path != "result.count" {
		t.Fatalf("recorded contract failure = %#v, want the counted violation with its path", failure)
	}
}

func TestResultContractSpendingDoesNotConsumeArgumentBudget(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	setTestResultContract(t, sub, testResultContractSchema)
	installContractFollowUpLLM(t, sub)
	// The contract budget is already spent.
	sub.turn.SubAgentResultContractRecoveryCount = 1

	// A blank summary is an argument error; it must still get its own follow-up.
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "   "}),
	})}})
	select {
	case evt := <-parent.eventCh:
		t.Fatalf("a spent contract budget terminated an argument error: %#v", evt)
	default:
	}
	if result := waitForSubAgentLLMResult(t, sub, time.Second); result.err != nil {
		t.Fatalf("follow-up request failed: %v", result.err)
	}
	if got := sub.turn.SubAgentCompletionRecoveryCount; got != 1 {
		t.Fatalf("argument recovery count = %d, want 1 despite the spent contract budget", got)
	}
}

func TestResultContractValidatesRefContent(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	setTestResultContract(t, sub, testResultContractSchema)
	// Spend the budget so the rejection is terminal and its diagnostics are
	// observable as the settlement input.
	sub.turn.SubAgentResultContractRecoveryCount = maxResultContractRecoveryAttempts

	// Store a payload that violates the contract, then submit it by ref: the
	// ref path must be checked against the stored content, not trusted.
	ref, _, err := tools.SaveImmutableResult(sub.sessionDir, "review_report", json.RawMessage(`{"name":"x","count":"two"}`))
	if err != nil {
		t.Fatalf("SaveImmutableResult: %v", err)
	}
	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{"summary": "done", "result_type": "review_report", "result_ref": ref}),
	})}})

	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError {
			t.Fatalf("event.Type = %q, want %q", evt.Type, EventAgentError)
		}
		err, ok := evt.Payload.(error)
		if !ok {
			t.Fatalf("payload = %#v, want an error", evt.Payload)
		}
		violation, ok := errors.AsType[*ResultContractViolationError](err)
		if !ok {
			t.Fatalf("payload = %T, want *ResultContractViolationError", err)
		}
		diag := violation.Diagnostics()
		if diag.ResultRef == nil || diag.ResultRef.ID != ref.ID {
			t.Fatalf("diagnostics result_ref = %#v, want %q", diag.ResultRef, ref.ID)
		}
		if len(diag.Violations) == 0 || diag.Violations[0].Path != "result.count" {
			t.Fatalf("diagnostics violations = %#v, want the stored payload's violation", diag.Violations)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the terminal contract violation")
	}
	assertCompleteCardStatus(t, parent, sub, "call-1", ToolResultStatusError)
	if failure := sub.resultContractFailure(); failure == nil || failure.ResultRef == nil || failure.ResultRef.ID != ref.ID {
		t.Fatalf("recorded contract failure = %#v, want the submitted ref", failure)
	}
}

func TestResultContractAcceptsUndeclaredFieldsWithoutRewriting(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	setTestResultContract(t, sub, testResultContractSchema)

	sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
		mustJSONToolCall(t, "call-1", "complete", map[string]any{
			"summary":     "done",
			"result_type": "review_report",
			"result":      map[string]any{"name": "x", "count": 1, "extra": map[string]any{"deep": true}},
		}),
	})}})

	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentDone {
			t.Fatalf("event.Type = %q, want %q (a contract is open, undeclared fields pass)", evt.Type, EventAgentDone)
		}
		result, ok := evt.Payload.(*AgentResult)
		if !ok || result.Envelope == nil {
			t.Fatalf("payload = %#v, want a completion envelope", evt.Payload)
		}
		if !strings.Contains(string(result.Envelope.Result), `"extra"`) {
			t.Fatalf("delivered result = %s, want the undeclared field preserved", result.Envelope.Result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the accepted completion")
	}
}

func TestResultContractViolationListIsBoundedAndCounted(t *testing.T) {
	const fieldCount = 150
	properties := make(map[string]any, fieldCount)
	required := make([]string, 0, fieldCount)
	for i := range fieldCount {
		name := fmt.Sprintf("field_%03d", i)
		properties[name] = map[string]any{"type": "string"}
		required = append(required, name)
	}
	raw, err := json.Marshal(map[string]any{"type": "object", "required": required, "properties": properties})
	if err != nil {
		t.Fatal(err)
	}
	schema, _, err := tools.CompileResultSchema(raw)
	if err != nil {
		t.Fatalf("CompileResultSchema: %v", err)
	}
	violations, err := tools.ValidateResultAgainstSchema(json.RawMessage(`{}`), schema)
	if err != nil {
		t.Fatalf("ValidateResultAgainstSchema: %v", err)
	}
	if len(violations) != fieldCount {
		t.Fatalf("violations = %d, want %d", len(violations), fieldCount)
	}

	violationErr := newResultContractViolationError(violations, nil, json.RawMessage(`{}`))
	diag := violationErr.Diagnostics()
	if len(diag.Violations) != maxResultContractDiagnosticViolations {
		t.Fatalf("diagnostic list = %d, want %d", len(diag.Violations), maxResultContractDiagnosticViolations)
	}
	if diag.RemainingViolations != fieldCount-maxResultContractDiagnosticViolations {
		t.Fatalf("remaining violations = %d, want %d", diag.RemainingViolations, fieldCount-maxResultContractDiagnosticViolations)
	}
	text := violationErr.Error()
	if !strings.Contains(text, fmt.Sprintf("%d violation(s)", fieldCount)) {
		t.Fatalf("rejection text = %q, want the total count", text)
	}
	if want := fmt.Sprintf("and %d more violation(s)", fieldCount-resultContractViolationLimit); !strings.Contains(text, want) {
		t.Fatalf("rejection text = %q, want %q", text, want)
	}
	// Confirmation item for the 20-violation cap: the worst-case render must
	// stay far below the tool-output truncation threshold (2000 lines / 50KB).
	if len(text) > 4096 {
		t.Fatalf("rejection text is %d bytes, want it bounded well below the truncation threshold", len(text))
	}
	if got := classifyAgentError(violationErr); got != agentErrorKindContract {
		t.Fatalf("classifyAgentError = %q, want %q", got, agentErrorKindContract)
	}
}

func TestResultContractDiagnosticValuesAreBounded(t *testing.T) {
	huge := `{"payload":"` + strings.Repeat("x", 8*1024) + `"}`
	violationErr := newResultContractViolationError([]tools.ResultSchemaViolation{{
		Invalid: message.InvalidToolArg{Path: "result.payload", Reason: message.InvalidToolArgReasonInvalid, ValueJSON: huge},
		Message: `result.payload must be a string, got object with 1 key(s)`,
	}}, nil, json.RawMessage(`{}`))

	diag := violationErr.Diagnostics()
	if len(diag.Violations) != 1 {
		t.Fatalf("diagnostic list = %d, want 1", len(diag.Violations))
	}
	value := diag.Violations[0].ValueJSON
	if len(value) >= len(huge) {
		t.Fatalf("value_json = %d bytes, want it bounded below the %d-byte node", len(value), len(huge))
	}
	if !strings.HasSuffix(value, "...") {
		t.Fatalf("value_json = %q, want an explicit truncation marker", value)
	}
	if want := 4*maxResultContractDiagnosticValueRunes + 3; len(value) > want {
		t.Fatalf("value_json = %d bytes, want at most %d bytes", len(value), want)
	}
	if diag.Violations[0].Path != "result.payload" {
		t.Fatalf("path = %q, want the violating path to survive truncation", diag.Violations[0].Path)
	}

	small := newResultContractViolationError([]tools.ResultSchemaViolation{{
		Invalid: message.InvalidToolArg{Path: "result.mode", Reason: message.InvalidToolArgReasonInvalid, ValueJSON: `"other"`},
		Message: `result.mode must be one of fast, slow, got string "other"`,
	}}, nil, json.RawMessage(`{}`))
	if got := small.Diagnostics().Violations[0].ValueJSON; got != `"other"` {
		t.Fatalf("value_json = %q, want an unchanged value below the bound", got)
	}
}

func TestResultContractIntegrityFailureIsTerminalWithoutBudget(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	setTestResultContract(t, sub, testResultContractSchema)

	missing := tools.ResultRef{
		ID:         "sha256-missing",
		ResultType: "review_report",
		RelPath:    "artifacts/results/sha256-missing.json",
		SHA256:     strings.Repeat("a", 64),
		SizeBytes:  10,
	}
	if err := sub.validateDeliveredResultAgainstContract(nil, &missing); err == nil {
		t.Fatal("an unreadable stored result must fail contract validation")
	} else if _, ok := errors.AsType[*ResultContractIntegrityError](err); !ok {
		t.Fatalf("error = %T, want *ResultContractIntegrityError", err)
	}

	sub.rejectInvalidCompleteArguments("call-1", &ResultContractIntegrityError{cause: errors.New("store corrupt"), resultRef: &missing}, nil)
	select {
	case evt := <-parent.eventCh:
		if evt.Type != EventAgentError {
			t.Fatalf("event.Type = %q, want %q", evt.Type, EventAgentError)
		}
		err, ok := evt.Payload.(error)
		if !ok {
			t.Fatalf("payload = %#v, want an error", evt.Payload)
		}
		if _, ok := errors.AsType[*ResultContractIntegrityError](err); !ok {
			t.Fatalf("payload = %T, want *ResultContractIntegrityError", err)
		}
	default:
		t.Fatal("an unreadable result store must be terminal")
	}
	if got := sub.turn.SubAgentResultContractRecoveryCount; got != 0 {
		t.Fatalf("contract recovery count = %d, want 0 (a retry cannot repair the store)", got)
	}
	if got := classifyAgentError(&ResultContractIntegrityError{cause: errors.New("x")}); got != agentErrorKindContract {
		t.Fatalf("classifyAgentError = %q, want %q", got, agentErrorKindContract)
	}
	assertCompleteCardStatus(t, parent, sub, "call-1", ToolResultStatusError)
	failure := sub.resultContractFailure()
	if failure == nil || failure.ResultRef == nil || failure.ResultRef.ID != missing.ID {
		t.Fatalf("recorded contract failure = %#v, want the unreadable ref", failure)
	}
}

func TestResultContractPersistsToDurableRecord(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-contract-persist")
	canonical := setTestResultContract(t, sub, testResultContractSchema)
	a.syncTaskRecordFromSub(sub, "")

	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil {
		t.Fatal("task record missing")
	}
	if string(rec.ResultSchema) != string(canonical) {
		t.Fatalf("recorded result_schema = %s, want %s", rec.ResultSchema, canonical)
	}
	// A re-delegation replaces the contract instead of inheriting it.
	sub.resultSchema = nil
	sub.resultSchemaJSON = nil
	a.syncTaskRecordFromSub(sub, "")
	if rec := a.taskRecordByTaskID(sub.taskID); len(rec.ResultSchema) != 0 {
		t.Fatalf("recorded result_schema = %s, want it cleared with the runtime contract", rec.ResultSchema)
	}
}

func TestMergeDurableTaskRecordsKeepsResultSchema(t *testing.T) {
	canonical := json.RawMessage(`{"type":"object"}`)
	base := map[string]*DurableTaskRecord{"t1": {TaskID: "t1", ResultSchema: canonical}}
	extra := map[string]*DurableTaskRecord{"t1": {TaskID: "t1", TaskDesc: "later snapshot"}}
	merged := mergeDurableTaskRecords(base, extra)
	got := merged["t1"]
	if got == nil || string(got.ResultSchema) != string(canonical) {
		t.Fatalf("merged record = %#v, want the schema carried over from the other snapshot", got)
	}
}

func TestResultContractRehydratesAndRendersPrompt(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.cachedWorkDir = a.projectRoot
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {Name: "worker", Mode: config.AgentModeSubAgent, Models: map[string][]string{"default": {"sample/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })

	_, canonical, err := tools.CompileResultSchema(json.RawMessage(testResultContractSchema))
	if err != nil {
		t.Fatal(err)
	}
	record := &DurableTaskRecord{
		TaskID:             "adhoc-contract-rehydrate",
		AgentDefName:       "worker",
		TaskDesc:           "contract work",
		State:              string(SubAgentStateCompleted),
		ResumePolicy:       taskResumePolicyNotify,
		LatestInstanceID:   "worker-old",
		InstanceHistory:    []string{"worker-old"},
		RuntimeParked:      true,
		ResultSchema:       canonical,
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})

	restored, _, err := a.rehydrateTask(record)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if !restored.hasResultContract() {
		t.Fatal("rehydrated worker lost its result contract")
	}
	if string(restored.resultSchemaJSON) != string(canonical) {
		t.Fatalf("rehydrated result_schema = %s, want %s", restored.resultSchemaJSON, canonical)
	}
	if prompt := restored.buildSystemPrompt(); !strings.Contains(prompt, string(canonical)) {
		t.Fatal("rehydrated system prompt must carry the result contract")
	}

	plain := newTestMainAgent(t, t.TempDir())
	plainSub := newControllableTestSubAgent(t, plain, "adhoc-no-contract")
	if strings.Contains(plainSub.buildSystemPrompt(), "### Result contract") {
		t.Fatal("a worker without a contract must not be told about one")
	}
}

func TestRehydrateDropsUnreadableResultContract(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.cachedWorkDir = a.projectRoot
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {Name: "worker", Mode: config.AgentModeSubAgent, Models: map[string][]string{"default": {"sample/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })

	record := &DurableTaskRecord{
		TaskID:           "adhoc-contract-unreadable",
		AgentDefName:     "worker",
		TaskDesc:         "contract work",
		State:            string(SubAgentStateCompleted),
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "worker-old",
		InstanceHistory:  []string{"worker-old"},
		RuntimeParked:    true,
		// A top-level array can never satisfy a delegated JSON-object result.
		ResultSchema:       json.RawMessage(`{"type":"array"}`),
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})

	restored, _, err := a.rehydrateTask(record)
	if err != nil {
		t.Fatalf("rehydrateTask must not fail on an unreadable contract: %v", err)
	}
	if restored.hasResultContract() {
		t.Fatal("an uncompilable contract must be dropped instead of enforced partially")
	}
	// The record follows the runtime: it must stop claiming a contract that can
	// no longer be enforced.
	a.syncTaskRecordFromSub(restored, "")
	if rec := a.taskRecordByTaskID(record.TaskID); len(rec.ResultSchema) != 0 {
		t.Fatalf("recorded result_schema = %s, want it cleared with the dropped contract", rec.ResultSchema)
	}
}

func TestCreateSubAgentRejectsUncompilableResultSchema(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 1)

	_, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description:  "work",
		AgentType:    "worker",
		ResultSchema: json.RawMessage(`{"type":"array"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "result_schema") {
		t.Fatalf("CreateSubAgent error = %v, want a result_schema rejection", err)
	}
}
