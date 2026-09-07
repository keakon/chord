package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

// typedResultPairingError marks the two failures that mean "the typed-result
// group is incomplete" rather than "the completion payload is unusable". They
// are the only Complete rejections that can be settled by dropping the group,
// because summary and every other structured field are still intact — see
// SubAgent.settleDegradedCompletion.
type typedResultPairingError struct{ err error }

func (e typedResultPairingError) Error() string { return e.err.Error() }
func (e typedResultPairingError) Unwrap() error { return e.err }

// typedResultShapeHint spells out where the fields live. Models that only read
// "result_type is required" have answered it by nesting result_type *inside*
// result and failing the same check twice, so the message names the level and
// gives the way out for completions that need no machine-readable result.
const typedResultShapeHint = " — they are all top-level Complete arguments, not fields inside result. " +
	`Either supply the group, for example {"summary": "…", "result_type": "review_report", "result": {…}}, ` +
	"or omit result_type, result and result_ref entirely."

// droppedTypedResultLimitation is recorded on a degraded delivery so the owner
// sees that a machine-readable result was attempted and did not survive, rather
// than silently receiving a completion with no result group.
const droppedTypedResultLimitation = "The machine-readable result was dropped: result_type and result/result_ref were not supplied together. " +
	"Summary and the remaining structured fields are complete."

func validateCompleteTypedResult(sessionDir, resultType string, result json.RawMessage, suppliedRef *tools.ResultRef) (string, json.RawMessage, *tools.ResultRef, error) {
	resultType = strings.TrimSpace(resultType)
	result = bytes.TrimSpace(result)
	if resultType == "" && len(result) == 0 && suppliedRef == nil {
		return "", nil, nil, nil
	}
	if resultType == "" {
		return "", nil, nil, typedResultPairingError{fmt.Errorf(
			"result_type, result, and result_ref must be provided together: result or result_ref requires result_type%s", typedResultShapeHint)}
	}
	var ref *tools.ResultRef
	if suppliedRef != nil {
		validated, err := tools.ValidateResultRef(sessionDir, *suppliedRef, resultType)
		if err != nil {
			return "", nil, nil, err
		}
		ref = &validated
	}
	if len(result) > 0 {
		if len(result) > tools.MaxInlineResultBytes {
			return "", nil, nil, fmt.Errorf("inline result exceeds maximum size %d bytes; use save_artifact with result and result_type, then pass result_ref", tools.MaxInlineResultBytes)
		}
		created, canonical, err := tools.SaveImmutableResult(sessionDir, resultType, result)
		if err != nil {
			return "", nil, nil, err
		}
		if ref != nil && created.ID != ref.ID {
			return "", nil, nil, fmt.Errorf("inline result does not match result_ref")
		}
		ref = &created
		result = canonical
	}
	if ref == nil {
		return "", nil, nil, typedResultPairingError{fmt.Errorf(
			"result_type, result, and result_ref must be provided together: result_type requires result or result_ref%s", typedResultShapeHint)}
	}
	return resultType, append(json.RawMessage(nil), result...), ref, nil
}
