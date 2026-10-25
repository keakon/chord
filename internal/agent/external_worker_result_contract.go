package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/schemaargs"
)

// errExternalResultNotMachineReadable is the contract failure for a final
// answer that is not a JSON object at all. The shared violation error's
// missing-result text names the internal `complete` tool, which the external
// process does not have, so this path carries its own message.
var errExternalResultNotMachineReadable = fmt.Errorf("result contract violation: the final answer is not a machine-readable JSON object; the result contract declared for this task requires the final answer to be a single JSON object satisfying it")

// hasResultContract reports whether this worker was admitted with a result
// contract. The external process has no `complete` tool: its final answer is
// the delivered result, so the contract is enforced on that text instead.
func (w *externalWorker) hasResultContract() bool {
	return w != nil && len(w.resultSchema) > 0
}

// externalResultContractPromptBlock tells the external process what its final
// answer must look like. It rides the same initialization channel as the role
// prompt, so the contract reaches the process before any task input is
// submitted, and a resumed turn re-declares it unchanged.
func (w *externalWorker) externalResultContractPromptBlock() string {
	if !w.hasResultContract() || len(w.resultSchemaJSON) == 0 {
		return ""
	}
	return strings.Join([]string{
		"### Result contract",
		"",
		"Your final answer must be a single JSON object and nothing else (no prose, no Markdown code fence) satisfying this JSON Schema:",
		"",
		"```json",
		string(w.resultSchemaJSON),
		"```",
		"",
		"The host validates your final result against this contract before accepting the task; a final answer that is not a JSON object satisfying it fails the task.",
	}, "\n")
}

// validateExternalResultContract checks the external process's final answer
// against the declared contract. There is no machine-readable delivery channel
// here, so the final text is the payload: text that is not valid JSON is a
// missing result, and a JSON document that does not satisfy the schema is
// reported with its violations, reusing the shared violation error so the
// rejection text matches the internal worker's.
func (w *externalWorker) validateExternalResultContract(text string) error {
	if !w.hasResultContract() {
		return nil
	}
	payload := json.RawMessage(strings.TrimSpace(text))
	if len(payload) == 0 {
		return errExternalResultNotMachineReadable
	}
	violations, err := schemaargs.ValidateResult(payload, w.resultSchema)
	if err != nil {
		// Not valid JSON, so there is no machine-readable result to report
		// violations against.
		return errExternalResultNotMachineReadable
	}
	if len(violations) == 0 {
		return nil
	}
	return newResultContractViolationError(violations, nil, payload)
}
