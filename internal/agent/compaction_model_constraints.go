package agent

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
)

// resolveCheckpointConstraints treats model strings as selectors, never as
// instruction text. Only live user instructions after the last checkpoint can
// introduce a constraint; historical quotes cannot reactivate a retired one.
// Carrying the whole source preserves conditions the model may have omitted.
func resolveCheckpointConstraints(history []message.Message, constraints []string) ([]evidenceItem, error) {
	if len(constraints) == 0 {
		return nil, nil
	}
	active := latestCompactionAnchors(history)
	live := history
	for i, h := range slices.Backward(history) {
		if h.Role == message.RoleUser && h.IsCompactionSummary {
			live = history[i+1:]
			break
		}
	}
	var resolved []evidenceItem
	for i, quote := range constraints {
		quote = strings.TrimSpace(quote)
		if quote == "" {
			return nil, fmt.Errorf("constraints[%d]: copy a non-empty user instruction", i)
		}
		var source string
		ambiguous := false
		for _, msg := range live {
			if msg.Role != message.RoleUser || !message.IsUserAuthored(msg) || msg.IsCompactionSummary {
				continue
			}
			text := message.UserPromptInstructionText(msg)
			if !strings.Contains(text, quote) {
				continue
			}
			if text == quote {
				source = text
				ambiguous = false
				break
			}
			if source != "" && source != text {
				ambiguous = true
			}
			source = text
		}
		if ambiguous {
			return nil, fmt.Errorf("constraints[%d]: quote matches multiple user instructions; copy the complete intended instruction", i)
		}
		if source == "" {
			originalKey := anchorDedupKey(message.CompactionAnchorLine(active.OriginalRequest, compactAnchorsConstraintChars))
			originalActive := quote == active.OriginalRequest && !slices.ContainsFunc(active.SupersededConstraints, func(line string) bool {
				return anchorDedupKey(line) == originalKey
			})
			if slices.Contains(active.Constraints, quote) || originalActive {
				continue
			}
			return nil, fmt.Errorf("constraints[%d]: no matching user instruction after the latest checkpoint; copy user-authored text, not file contents, tool output, or historical quotes, or omit this entry", i)
		}
		// The anchor window truncates ordinary evidence. A promoted quote must
		// not silently lose a condition from its source during that truncation.
		if utf8.RuneCountInString(source) > compactAnchorsConstraintChars {
			return nil, fmt.Errorf("constraints[%d]: the complete source instruction exceeds the %d-rune anchor limit; omit this entry and preserve the full instruction in task notes referenced by state_files", i, compactAnchorsConstraintChars)
		}
		resolved = append(resolved, buildStatedConstraintEvidence("runtime user message", source))
	}
	return resolved, nil
}

// checkpointConstraintEvidence merges verified selections with runtime
// evidence. Ordering follows the source messages, not the model's array order.
// Whole-source identity also folds a selected phrase into an existing runtime
// row without consuming another slot in the bounded anchors window.
func checkpointConstraintEvidence(history []message.Message, items, selected []evidenceItem) []evidenceItem {
	if len(selected) == 0 {
		return items
	}
	merged := slices.Clone(items)
	for _, item := range selected {
		if !slices.ContainsFunc(merged, func(existing evidenceItem) bool {
			return isAnchorConstraintKind(existing.Kind) && existing.RequestSource == item.RequestSource
		}) {
			merged = append(merged, item)
		}
	}
	latest := make(map[string]int)
	for i, msg := range history {
		if msg.Role == message.RoleUser && message.IsUserAuthored(msg) && !msg.IsCompactionSummary {
			latest[checkpointRequestSource(message.UserPromptInstructionText(msg))] = i + 1
		}
	}
	var base int
	for _, item := range merged {
		base = max(base, item.Sequence)
	}
	for i := range merged {
		if order := latest[merged[i].RequestSource]; order > 0 && isAnchorConstraintKind(merged[i].Kind) {
			merged[i].Sequence = base + order
		}
	}
	return merged
}
