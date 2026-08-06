package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

func validateCompleteTypedResult(sessionDir, resultType string, result json.RawMessage, suppliedRef *tools.ResultRef) (string, json.RawMessage, *tools.ResultRef, error) {
	resultType = strings.TrimSpace(resultType)
	result = bytes.TrimSpace(result)
	if resultType == "" && len(result) == 0 && suppliedRef == nil {
		return "", nil, nil, nil
	}
	if resultType == "" {
		return "", nil, nil, fmt.Errorf("result_type is required when result or result_ref is provided")
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
			return "", nil, nil, fmt.Errorf("inline result exceeds maximum size %d bytes; use save_result and provide result_ref", tools.MaxInlineResultBytes)
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
		return "", nil, nil, fmt.Errorf("result or result_ref is required when result_type is provided")
	}
	return resultType, append(json.RawMessage(nil), result...), ref, nil
}
