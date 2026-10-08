package tui

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/markdownutil"
)

type typedCheckpointDisplayClaim struct {
	Kind         string   `json:"kind"`
	EvidenceRefs []string `json:"evidence_refs"`
	Status       string   `json:"status"`
}

type typedCheckpointDisplayState struct {
	Completed         []string                               `json:"completed"`
	Decisions         []string                               `json:"decisions"`
	OpenIssues        []string                               `json:"open_issues"`
	CarriedOpenIssues []string                               `json:"carried_open_issues"`
	EvidenceRefs      []string                               `json:"evidence_refs"`
	StageID           string                                 `json:"stage_id"`
	StageStatus       string                                 `json:"stage_status"`
	CheckpointKind    string                                 `json:"checkpoint_kind"`
	Claims            map[string]typedCheckpointDisplayClaim `json:"claims"`
}

// wrapCompactionTypedStateForDisplay renders the machine-carryable checkpoint
// state as readable fields and lists. The rewrite is display-only; persisted
// checkpoint content remains the authoritative carry format, and the copy
// action still yields the verbatim payload.
//
// The rewrite is deliberately narrow: it needs the heading on its own column-0
// line followed by a bullet whose value is a complete JSON document, so prose
// that merely quotes the heading is left untouched. A payload that parses as
// JSON but cannot be projected (not a JSON object, or a field of the wrong
// shape) keeps its fenced JSON block, because no structured form exists to
// read.
func wrapCompactionTypedStateForDisplay(body string) string {
	if !strings.Contains(body, message.CompactionTypedStateHeading) {
		return body
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines)+2)
	var open markdownutil.Fence
	inFence := false
	changed := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		out = append(out, line)
		if inFence {
			if markdownutil.IsFenceClose(line, open) {
				inFence = false
			}
			continue
		}
		if fence, ok := markdownutil.ParseFenceLine(line); ok {
			open, inFence = fence, true
			continue
		}
		if line != message.CompactionTypedStateHeading {
			continue
		}
		payload := i + 1
		for payload < len(lines) && strings.TrimSpace(lines[payload]) == "" {
			payload++
		}
		if payload >= len(lines) {
			continue
		}
		value, ok := strings.CutPrefix(lines[payload], "- ")
		value = strings.TrimSpace(value)
		if !ok || !json.Valid([]byte(value)) {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(value), &object); err != nil || object == nil {
			out = append(out, lines[i+1:payload]...)
			out = append(out, "```json", value, "```")
			i = payload
			changed = true
			continue
		}
		var state typedCheckpointDisplayState
		if err := json.Unmarshal([]byte(value), &state); err != nil {
			out = append(out, lines[i+1:payload]...)
			out = append(out, "```json", value, "```")
			i = payload
			changed = true
			continue
		}
		out = append(out, lines[i+1:payload]...)
		out = append(out, renderTypedCheckpointDisplay(state)...)
		i = payload
		changed = true
	}
	if !changed {
		return body
	}
	return strings.Join(out, "\n")
}

func renderTypedCheckpointDisplay(state typedCheckpointDisplayState) []string {
	lines := []string{
		"### Stage",
		"- ID: " + typedCheckpointDisplayValue(state.StageID),
		"- Status: " + typedCheckpointDisplayValue(state.StageStatus),
		"- Checkpoint kind: " + typedCheckpointDisplayValue(state.CheckpointKind),
	}
	appendList := func(heading string, items []string, emptyLabel string) {
		lines = append(lines, "", "### "+heading)
		if len(items) == 0 {
			lines = append(lines, "- "+emptyLabel)
			return
		}
		for _, item := range items {
			lines = append(lines, "- "+typedCheckpointDisplayValue(item))
		}
	}
	appendList("Completed", state.Completed, "None recorded")
	appendList("Decisions", state.Decisions, "None recorded")
	appendList("Open Issues", state.OpenIssues, "None recorded")
	appendList("Carried Open Issues", state.CarriedOpenIssues, "None carried")
	appendList("Evidence References", state.EvidenceRefs, "None recorded")

	lines = append(lines, "", "### Claims")
	if len(state.Claims) == 0 {
		lines = append(lines, "- None recorded")
	} else {
		claimNames := make([]string, 0, len(state.Claims))
		for claim := range state.Claims {
			claimNames = append(claimNames, claim)
		}
		slices.Sort(claimNames)
		for _, claim := range claimNames {
			lines = append(lines, "- "+typedCheckpointDisplayValue(claim))
			item := state.Claims[claim]
			if item.Kind != "" {
				lines = append(lines, "  - Kind: "+typedCheckpointDisplayValue(item.Kind))
			}
			if item.Status != "" {
				lines = append(lines, "  - Status: "+typedCheckpointDisplayValue(item.Status))
			}
			if len(item.EvidenceRefs) > 0 {
				refs := make([]string, len(item.EvidenceRefs))
				for i, ref := range item.EvidenceRefs {
					refs[i] = typedCheckpointDisplayValue(ref)
				}
				lines = append(lines, "  - Evidence: "+strings.Join(refs, ", "))
			}
		}
	}
	return lines
}

func typedCheckpointDisplayValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(not recorded)"
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Join(strings.Split(value, "\n"), " ⏎ ")
	var escaped strings.Builder
	for _, r := range value {
		if strings.ContainsRune("\\`*_{}[]()#+-.!|>~<&", r) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}
