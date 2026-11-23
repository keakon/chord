package llm

import (
	"encoding/json"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"
)

func logResponsesReasoningRemovals(responseID string, report json.RawMessage) {
	if !log.IsEnabledFor(golog.DebugLevel) {
		return
	}
	// Decode independently of the terminal output so a relay can omit the
	// diagnostic or report an unexpected shape without failing the response.
	var entries []json.RawMessage
	if len(report) == 0 || json.Unmarshal(report, &entries) != nil || entries == nil {
		log.Debugf("responses: reasoning replay response_id=%q input_transformations=unavailable", responseID)
		return
	}
	counts := make(map[string]int)
	total := 0
	for _, raw := range entries {
		var entry struct {
			Type   string          `json:"type"`
			Action string          `json:"action"`
			Reason json.RawMessage `json:"reason"`
		}
		if json.Unmarshal(raw, &entry) != nil || entry.Type != "reasoning" || entry.Action != "removed" {
			continue
		}
		var reason string
		_ = json.Unmarshal(entry.Reason, &reason)
		switch reason {
		case "organization_mismatch", "end_user_mismatch", "model_mismatch", "prefix_mismatch", "turn_boundary":
		default:
			reason = "other"
		}
		counts[reason]++
		total++
	}
	log.Debugf("responses: reasoning replay response_id=%q input_transformations=reported reasoning_items_removed=%d removals_by_reason=%v", responseID, total, counts)
}
