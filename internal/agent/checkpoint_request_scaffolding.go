package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/agent/checkpointfmt"
	"github.com/keakon/chord/internal/message"
)

const checkpointWorklogRequestIntro = "Historical calls; newer observations take precedence. Shell tool success does not prove that a background process has finished."

// Project only runtime-rendered sections before the archive wrapper. Quoted
// headings in retained messages and evidence must remain verbatim.
func projectCheckpointScaffolding(body string, modelDriven bool) string {
	end := len(body)
	for _, marker := range []string{message.CompactionCompressedTag, retainedRecentMessagesHeading, message.CompactionEvidenceTag} {
		if pos := strings.Index(body[:end], marker); pos >= 0 {
			end = pos
		}
	}
	summary, rest := body[:end], body[end:]
	if start, end, ok := checkpointfmt.SectionBounds(summary, checkpointWorklogHeading); ok {
		section := strings.TrimSpace(summary[start:end])
		if projected := projectCheckpointWorklog(section); projected != section {
			replacement := ""
			if projected != "" {
				replacement = checkpointWorklogHeading + "\n" + projected
			}
			summary = spliceCheckpointSection(summary, start-len(checkpointWorklogHeading), end, replacement)
		}
	}
	summary = projectCheckpointTodoSkeleton(summary)
	if modelDriven {
		for _, placeholder := range []struct{ heading, content string }{
			{"## Files and Evidence", checkpointArchivePointer},
			{"## SubAgent State", formatSubAgentsAsBullets(nil)},
			{checkpointfmt.UserConstraintsHeading, checkpointNoUserConstraints},
		} {
			if start, end, ok := checkpointfmt.SectionBounds(summary, placeholder.heading); ok && strings.TrimSpace(summary[start:end]) == placeholder.content {
				summary = spliceCheckpointSection(summary, start-len(placeholder.heading), end, "")
			}
		}
		// The mode explanation is diagnostic metadata. Keep the archive's
		// authority and paths, without repeating how the runtime built it.
		if tail, ok := strings.CutPrefix(rest, message.CompactionCompressedTag+"\n"+checkpointModelDrivenModeNote); ok {
			rest = message.CompactionCompressedTag + "\nRuntime checkpoint with model-declared continuation state. Archived history is authoritative.\n" + tail
		}
	}
	if summary == body[:end] && rest == body[end:] {
		return body
	}
	if rest == "" {
		return summary
	}
	return strings.TrimRight(summary, "\n") + "\n\n" + rest
}

// Successful shell calls are not verification conclusions: they can merely
// start a job. Keep their count and archive access, plus every non-success row.
func projectCheckpointWorklog(section string) string {
	content, omitted := strings.CutSuffix(section, "\n\n"+checkpointWorklogOmittedNote)
	blocks := strings.Split(content, "\n### ")
	if strings.TrimSpace(blocks[0]) != checkpointWorklogIntro {
		return section
	}
	kept := []string{checkpointWorklogRequestIntro}
	for _, block := range blocks[1:] {
		title, rows, _ := strings.Cut(block, "\n")
		switch strings.TrimSpace(title) {
		case checkpointWorklogFilesTitle:
			continue
		case checkpointWorklogCommandsTitle:
			var lines []string
			successes := 0
			for line := range strings.SplitSeq(rows, "\n") {
				if strings.HasPrefix(line, "- ["+message.ToolStatusSuccess+"] ") {
					successes++
				} else {
					lines = append(lines, line)
				}
			}
			if successes > 0 {
				lines = append(lines, fmt.Sprintf("- %d successful shell call(s); commands and results are in archived history.", successes))
				projected := strings.Join(lines, "\n")
				if len(projected) < len(rows) {
					block = title + "\n" + projected
				}
			}
		}
		kept = append(kept, strings.TrimSpace(block))
	}
	if len(kept) == 1 {
		return ""
	}
	result := strings.Join(kept, "\n\n### ")
	if omitted {
		result += "\n\n" + checkpointWorklogOmittedNote
	}
	return result
}

func projectCheckpointTodoSkeleton(summary string) string {
	const heading = "## Todo State"
	start, end, ok := checkpointfmt.SectionBounds(summary, heading)
	if !ok {
		return summary
	}
	section := strings.TrimSpace(summary[start:end])
	classification, snapshot, hasSnapshot := strings.Cut(section, "\n### Runtime TODO snapshot")
	classification = strings.TrimSpace(classification)
	// Actual classifications, especially a Done rejection's stale entries,
	// carry restore semantics and must not be inferred from todo status.
	if classification != formatTodosAsRelevanceBullets(nil, fallbackAnchor{Text: "request"}) &&
		classification != formatTodosAsRelevanceBullets(nil, fallbackAnchor{}) {
		return summary
	}
	replacement := ""
	if hasSnapshot {
		replacement = heading + "\n### Runtime TODO snapshot" + snapshot
	}
	return spliceCheckpointSection(summary, start-len(heading), end, replacement)
}
