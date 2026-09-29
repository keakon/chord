package agent

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// projectCheckpointRequests changes only request-local copies. The durable
// checkpoint remains the source for typed carry, resume and evidence recovery.
func projectCheckpointRequests(messages []message.Message) []message.Message {
	out := messages
	copied := false
	for i, msg := range messages {
		if msg.Role != message.RoleUser || !msg.IsCompactionSummary || len(msg.Parts) != 0 {
			continue
		}
		body := deduplicateCheckpointRequest(msg.Content)
		if msg.CompactionSummaryMode == compactionSummaryModeModelDriven {
			body = checkpointRequestBody(msg.Content)
		}
		if body == msg.Content {
			continue
		}
		if !copied {
			out = slices.Clone(messages)
			copied = true
		}
		out[i].Content = body
	}
	return out
}

func checkpointRequestBody(body string) string {
	// Generic or incomplete recovery summaries can carry facts only in JSON.
	for _, heading := range []string{checkpointProgressHeading, checkpointKeyDecisionsHeading, checkpointOpenProblemsHeading} {
		if findMarkdownHeadingLine(body, heading) < 0 {
			return deduplicateCheckpointRequest(body)
		}
	}
	// Remove only a parseable machine payload; preserve any carry-loss notices
	// following it, and leave malformed blocks visible for recovery.
	for _, r := range slices.Backward(typedStateSectionRanges(body)) {
		section := body[r.headingStart:r.contentEnd]
		if _, found, malformed := typedStateFromBody(section); !found || malformed {
			continue
		}
		content := body[r.contentStart:r.contentEnd]
		payload := typedStateJSONLine(content)
		if payload == "" {
			continue
		}
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-")) != payload {
				continue
			}
			rest := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
			if rest != "" {
				rest = "## Checkpoint Carry Notices\n" + rest + "\n\n"
			}
			body = body[:r.headingStart] + rest + body[r.contentEnd:]
			break
		}
	}
	return deduplicateCheckpointRequest(body)
}

func deduplicateCheckpointRequest(body string) string {
	section, ok := compactionCurrentUserRequestSection(body)
	const prefix = "- " + latestUserRequestLabel + ": "
	if !ok || !strings.HasPrefix(section, prefix) || strings.Contains(section, "\n"+truncatedRequestNotice) {
		return body
	}
	request := strings.ReplaceAll(strings.TrimPrefix(section, prefix), "\n  ", "\n")
	// Match the canonical complete rendering, not semantically similar user
	// messages, inherited anchors, corrections, or truncated request excerpts.
	if section != modelDrivenCurrentUserRequestSection(fallbackAnchor{Kind: "user_request", Label: latestUserRequestLabel, Text: request}) {
		return body
	}
	start := findMarkdownHeadingLine(body, retainedRecentMessagesHeading)
	if start < 0 {
		return body
	}
	end := len(body)
	if n := strings.Index(body[start:], "\n"+message.CompactionEvidenceTag); n >= 0 {
		end = start + n
	}
	retained := body[start:end]
	quote := "\n" + retainedUserLabel + ":\n> " + strings.ReplaceAll(request, "\n", "\n> ")
	pos := strings.Index(retained, quote)
	if pos < 0 {
		return body
	}
	after := pos + len(quote)
	suffix := retained[after:]
	if suffix != "" && !strings.HasPrefix(suffix, "\n\n") && suffix != "\n" {
		return body
	}
	retained = retained[:pos] + "\n" + retainedUserLabel + ": Same request as Current User Request above." + suffix
	return body[:start] + retained + body[end:]
}
