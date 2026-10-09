package agent

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
)

const (
	checkpointOriginalRequestSource      = "original_request"
	checkpointConstraintSourcePrefix     = "constraint:"
	checkpointUserConstraintSourcePrefix = "user_constraint:"
	checkpointEvidenceSourcePrefix       = "evidence:"
)

// Request sources are durable provenance, not prompt text. Rendered excerpts
// cannot recover this information because truncation discards source text.
func checkpointRequestSource(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
}

func checkpointConstraintSourceKey(line string) string {
	return checkpointConstraintSourcePrefix + checkpointRequestSource(line)
}

func checkpointUserConstraintSourceKey(excerpt string) string {
	return checkpointUserConstraintSourcePrefix + checkpointRequestSource(excerpt)
}

func requestEvidenceSource(item evidenceItem, text string) evidenceItem {
	item.RequestSource = checkpointRequestSource(text)
	// Two full requests with identical snippets are still distinct evidence.
	item.Key = string(item.Kind) + "\x00" + item.RequestSource
	return item
}

func buildUserCorrectionEvidence(source, text string) evidenceItem {
	return requestEvidenceSource(buildEvidenceItem(
		evidenceUserCorrection,
		"User correction / constraint",
		"This explicitly constrains the next code change and should be preserved verbatim.",
		source,
		compactTextSnippet(text, compactUserConstraintExcerptChars),
	), text)
}

// buildCheckpointRequestSources records the source of each visible anchor and
// instruction evidence row. A carried anchor keeps its prior source even when
// a newer, different request renders to the same excerpt.
func buildCheckpointRequestSources(body string, history []message.Message, original string, items []evidenceItem) map[string]string {
	anchors := parseCompactionAnchors(message.CompactionAnchorsSection(body))
	var prior compactionAnchors
	var priorSources map[string]string
	for _, msg := range slices.Backward(history) {
		if msg.Role == message.RoleUser && msg.IsCompactionSummary {
			prior = parseCompactionAnchors(message.CompactionAnchorsSection(msg.Content))
			priorSources = msg.CompactionRequestSources
			break
		}
	}
	sources := make(map[string]string)
	if anchors.OriginalRequest != "" {
		if anchors.OriginalRequest == prior.OriginalRequest {
			if source := priorSources[checkpointOriginalRequestSource]; source != "" {
				sources[checkpointOriginalRequestSource] = source
			}
		} else if prior.OriginalRequest == "" && anchors.OriginalRequest == anchorLine(original, compactAnchorsRequestChars) {
			sources[checkpointOriginalRequestSource] = checkpointRequestSource(original)
		}
	}
	for _, line := range anchors.Constraints {
		key := checkpointConstraintSourceKey(line)
		if slices.Contains(prior.Constraints, line) {
			if source := priorSources[key]; source != "" {
				sources[key] = source
			}
			continue
		}
		var source string
		ambiguous := false
		for _, item := range items {
			if !isAnchorConstraintKind(item.Kind) || anchorLine(item.Excerpt, compactAnchorsConstraintChars) != line {
				continue
			}
			if item.RequestSource == "" || source != "" && source != item.RequestSource {
				ambiguous = true
			}
			source = item.RequestSource
		}
		if source != "" && !ambiguous {
			sources[key] = source
		}
	}
	for _, item := range items {
		if isAnchorConstraintKind(item.Kind) && item.RequestSource != "" {
			sources[checkpointEvidenceSourcePrefix+evidenceItemID(item)] = item.RequestSource
		}
	}
	// User Constraints is rendered directly from correction evidence. If two
	// different sources flatten to the same bullet, neither is attributable.
	for _, item := range items {
		if item.Kind != evidenceUserCorrection {
			continue
		}
		key := checkpointUserConstraintSourceKey(strings.ReplaceAll(item.Excerpt, "\n", " "))
		if prior, exists := sources[key]; exists && prior != item.RequestSource {
			sources[key] = ""
		} else if !exists {
			sources[key] = item.RequestSource
		}
	}
	for key, source := range sources {
		if source == "" {
			delete(sources, key)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	return sources
}
