package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// Result contract failure categories, surfaced in TaskContractDiagnostics.
// MissingResult means the completion carried no machine-readable result at all;
// SchemaViolation means a delivered result did not satisfy the contract.
const (
	resultContractCategoryMissingResult = "missing_result"
	resultContractCategorySchemaFailure = "schema_violation"
)

// resultContractViolationLimit bounds how many violations the rejection text
// names before it summarizes the rest. The rejection is read by the worker and
// the owner, so it stays short even when the delivered result is badly shaped.
const resultContractViolationLimit = 20

// maxResultContractDiagnosticViolations bounds the machine-readable list that
// travels on the error and into TaskSettlement. The validator collects every
// violation, so without a bound a pathological payload could put an unbounded
// list into the settlement journal; the count beyond the bound is always
// reported, so truncation is never silent.
const maxResultContractDiagnosticViolations = 100

// maxResultContractDiagnosticValueRunes bounds the raw node text a single
// diagnostic entry carries. A reported node can be as large as the delivered
// result (ref-delivered results reach maxImmutableResultBytes), and the list is
// persisted twice — the settlement journal and the task registry record — so the
// entry count alone would not bound the record. Truncation keeps an explicit
// marker, and the full node stays readable through the result ref.
const maxResultContractDiagnosticValueRunes = 512

// TaskContractDiagnostics is the machine-readable record of why a delegated
// result contract was not satisfied. It travels on the terminal error and is
// copied into TaskSettlement, the durable surface resume and headless read.
type TaskContractDiagnostics struct {
	Category            string                   `json:"category"`
	Violations          []message.InvalidToolArg `json:"violations,omitempty"`
	RemainingViolations int                      `json:"remaining_violations,omitempty"`
	ResultRef           *tools.ResultRef         `json:"result_ref,omitempty"`
	InlineResultPreview string                   `json:"inline_result_preview,omitempty"`
}

func cloneTaskContractDiagnostics(in *TaskContractDiagnostics) *TaskContractDiagnostics {
	if in == nil {
		return nil
	}
	out := *in
	out.Category = strings.TrimSpace(out.Category)
	out.Violations = append([]message.InvalidToolArg(nil), out.Violations...)
	if out.ResultRef != nil {
		ref := *out.ResultRef
		out.ResultRef = &ref
	}
	return &out
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

// ResultContractViolationError reports a completion whose delivered result does
// not satisfy the contract the owner declared at delegation. It is the
// repairable class: the worker gets one bounded chance to correct the result
// before this error becomes the task's terminal outcome.
type ResultContractViolationError struct {
	diagnostics TaskContractDiagnostics
	violations  []tools.ResultSchemaViolation
}

// Diagnostics returns the machine-readable failure record: the violation list
// keeps up to maxResultContractDiagnosticViolations entries, and
// RemainingViolations counts the rest so the bound is never a silent drop.
// Callers that store it must clone it first.
func (e *ResultContractViolationError) Diagnostics() TaskContractDiagnostics {
	if e == nil {
		return TaskContractDiagnostics{}
	}
	return e.diagnostics
}

func (e *ResultContractViolationError) Error() string {
	if e == nil {
		return "result contract violation"
	}
	if e.diagnostics.Category == resultContractCategoryMissingResult {
		return "result contract violation: the completion delivered no machine-readable result; " +
			"call complete with result_type together with result or result_ref"
	}
	shown := e.violations
	if len(shown) > resultContractViolationLimit {
		shown = shown[:resultContractViolationLimit]
	}
	parts := make([]string, 0, len(shown))
	for _, violation := range shown {
		parts = append(parts, violation.Message)
	}
	text := fmt.Sprintf("result contract violation: %d violation(s): %s", len(e.violations), strings.Join(parts, "; "))
	if remaining := len(e.violations) - len(shown); remaining > 0 {
		text += fmt.Sprintf("; and %d more violation(s)", remaining)
	}
	return text + ". The delivered result must satisfy the result contract declared for this task."
}

// ResultContractIntegrityError reports that the immutable result store could
// not back the completion: the ref was validated, but its content did not read
// back as JSON. Retrying cannot repair the store, so this failure is terminal
// and does not spend the contract repair budget.
type ResultContractIntegrityError struct {
	resultRef *tools.ResultRef
	cause     error
}

func (e *ResultContractIntegrityError) Diagnostics() TaskContractDiagnostics {
	diag := TaskContractDiagnostics{Category: resultContractCategorySchemaFailure}
	if e == nil || e.resultRef == nil {
		return diag
	}
	ref := *e.resultRef
	diag.ResultRef = &ref
	return diag
}

func (e *ResultContractIntegrityError) Error() string {
	if e == nil {
		return "result contract integrity failure"
	}
	target := "the delivered result"
	if e.resultRef != nil {
		target = fmt.Sprintf("stored result %q", e.resultRef.ID)
	}
	return fmt.Sprintf("result contract integrity failure: %s could not be read back as JSON: %v", target, e.cause)
}

func (e *ResultContractIntegrityError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// hasResultContract reports whether this worker was admitted with a result
// contract; without one every completion path behaves exactly as before.
func (s *SubAgent) hasResultContract() bool {
	return s != nil && len(s.resultSchema) > 0
}

// missingResultContractError is the contract failure for a completion that
// carries no machine-readable result at all. Without a contract that shape is
// repaired by dropping the typed-result group; with one the result is the
// deliverable, so the same shape is a violation that cannot be degraded.
func (s *SubAgent) missingResultContractError() error {
	if !s.hasResultContract() {
		return nil
	}
	return &ResultContractViolationError{diagnostics: TaskContractDiagnostics{Category: resultContractCategoryMissingResult}}
}

// validateDeliveredResultAgainstContract checks a delivered result against the
// declared contract. inline is the canonical inline payload (nil when the
// delivery used result_ref); ref is the validated ref (nil when inline). Both
// submission paths are checked, so storing a non-conforming payload first and
// submitting it through result_ref cannot bypass the contract.
func (s *SubAgent) validateDeliveredResultAgainstContract(inline json.RawMessage, ref *tools.ResultRef) error {
	if !s.hasResultContract() {
		return nil
	}
	payload := inline
	if ref != nil {
		content, err := tools.LoadResultContent(s.sessionDir, *ref)
		if err != nil {
			return &ResultContractIntegrityError{cause: err, resultRef: ref}
		}
		payload = content
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		// No typed result was delivered. That is a missing result the worker
		// can still supply, not a store that failed to read back.
		return s.missingResultContractError()
	}
	violations, err := tools.ValidateResultAgainstSchema(payload, s.resultSchema)
	if err != nil {
		return &ResultContractIntegrityError{cause: err, resultRef: ref}
	}
	if len(violations) == 0 {
		return nil
	}
	return newResultContractViolationError(violations, ref, inline)
}

func newResultContractViolationError(violations []tools.ResultSchemaViolation, ref *tools.ResultRef, inline json.RawMessage) *ResultContractViolationError {
	diag := TaskContractDiagnostics{
		Category:   resultContractCategorySchemaFailure,
		Violations: make([]message.InvalidToolArg, 0, len(violations)),
	}
	for _, violation := range violations {
		if len(diag.Violations) >= maxResultContractDiagnosticViolations {
			diag.RemainingViolations++
			continue
		}
		entry := violation.Invalid
		entry.ValueJSON = truncateString(entry.ValueJSON, maxResultContractDiagnosticValueRunes)
		diag.Violations = append(diag.Violations, entry)
	}
	if ref != nil {
		refCopy := *ref
		diag.ResultRef = &refCopy
	} else {
		diag.InlineResultPreview = string(truncateResultContractPreview(inline))
	}
	return &ResultContractViolationError{diagnostics: diag, violations: violations}
}

// truncateResultContractPreview bounds the inline payload recorded in the
// terminal diagnostics to the same magnitude the completion path already
// accepts inline, so the settlement never carries an unbounded payload.
func truncateResultContractPreview(inline json.RawMessage) json.RawMessage {
	if len(inline) <= tools.MaxInlineResultBytes {
		return inline
	}
	return inline[:tools.MaxInlineResultBytes]
}

// setResultContractFailure records the terminal contract failure for the
// settlement. It runs on the worker's run loop and is read when the task
// settles on the main event loop, so the field is mutex-guarded.
func (s *SubAgent) setResultContractFailure(diag TaskContractDiagnostics) {
	if s == nil {
		return
	}
	cloned := cloneTaskContractDiagnostics(&diag)
	s.resultContractMu.Lock()
	s.resultContract = cloned
	s.resultContractMu.Unlock()
}

func (s *SubAgent) resultContractFailure() *TaskContractDiagnostics {
	if s == nil {
		return nil
	}
	s.resultContractMu.Lock()
	defer s.resultContractMu.Unlock()
	return cloneTaskContractDiagnostics(s.resultContract)
}

// resultContractPromptBlock tells the worker what the delegated result must look
// like. It is appended to "## Your Task" because the contract is part of the
// task definition, while the complete tool's own declaration stays
// task-agnostic so the tool surface and its prompt prefix cache are unaffected.
func (s *SubAgent) resultContractPromptBlock() string {
	if !s.hasResultContract() || len(s.resultSchemaJSON) == 0 {
		return ""
	}
	return strings.Join([]string{
		"### Result contract",
		"",
		"call complete with a machine-readable result that satisfies this JSON Schema:",
		"",
		"```json",
		string(s.resultSchemaJSON),
		"```",
		"",
		"Supply it as result_type together with result or result_ref; the engine checks the delivered result against this contract before accepting complete.",
	}, "\n")
}
