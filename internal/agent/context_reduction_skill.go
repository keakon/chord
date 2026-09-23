package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

// A loaded skill body is instruction state, not data. Chord wraps every load in
// a <skill> envelope whose anchors (<path>, <root>, <relative_paths_base>) name
// the copy that was actually loaded — the same skill name can exist in several
// load roots (project .chord/skills, nested .agents/skills, the config home,
// configured extra paths) and the loader keeps the first occurrence, so the
// name alone does not say which files the workflow's relative references point
// at. Request-level reduction therefore summarizes a skill as instructions —
// anchors plus the workflow outline — instead of letting a content-shape rule
// (search hits, numbered source, log markers) describe it as data and drop the
// root. The full text stays recoverable through the archive address the
// reduction layer appends.
const (
	// skillSkeletonMaxHeadings bounds how much of the workflow outline survives
	// the summary. A deeper outline is still readable at the archive address;
	// the outline exists to keep the model oriented, not to restate the body.
	// 20 headings stay well under the stale-output budget (1500 bytes) the
	// skill summary gate uses, so the outline cannot itself become a payload.
	skillSkeletonMaxHeadings = 20
	// skillSkeletonMaxChars bounds the outline's byte cost so a heading-heavy
	// skill cannot turn its own summary back into a large payload. 600 bytes
	// is under half the stale budget and a fifth of the read-like budget
	// (3000 bytes), leaving room for the four envelope anchors the summary
	// always keeps.
	skillSkeletonMaxChars = 600
)

// isSkillInstructionResult reports whether one tool result is a loaded skill
// body, identified by the <root> anchor the envelope always carries. A failed
// load has no envelope and keeps the ordinary error classification.
func isSkillInstructionResult(ctx requestReductionContext) bool {
	if toolname.Normalize(ctx.ToolName) != tools.NameSkill {
		return false
	}
	_, ok := skillEnvelopeField(ctx.Content, "root")
	return ok
}

// reduceSkillInstructionSummary renders a reduced skill body: the envelope
// anchors that keep the workflow's paths resolvable, plus its heading outline.
func reduceSkillInstructionSummary(ctx requestReductionContext) string {
	var sb strings.Builder
	sb.WriteString("[Older skill output summarized for this request; the full text stays readable at the address below]\n")
	for _, tag := range []string{"name", "path", "root", "relative_paths_base"} {
		writeSkillEnvelopeField(&sb, ctx.Content, tag)
	}
	if headings := skillWorkflowHeadings(ctx.Content); len(headings) > 0 {
		sb.WriteString("Workflow headings:\n")
		for _, heading := range headings {
			sb.WriteString("- ")
			sb.WriteString(heading)
			sb.WriteString("\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func writeSkillEnvelopeField(sb *strings.Builder, content, tag string) {
	value, ok := skillEnvelopeField(content, tag)
	if !ok {
		return
	}
	fmt.Fprintf(sb, "<%s>%s</%s>\n", tag, value, tag)
}

// skillEnvelopeField reads one field out of a <skill> envelope. The envelope's
// fields precede the body, so the first occurrence is the anchor; a body that
// mentions the same tag later is left alone. A value that never closes is
// treated as absent rather than read to the end of the content.
func skillEnvelopeField(content, tag string) (string, bool) {
	_, rest, ok := strings.Cut(content, "<"+tag+">")
	if !ok {
		return "", false
	}
	value, _, ok := strings.Cut(rest, "</"+tag+">")
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

// skillWorkflowHeadings extracts the markdown heading outline: which phases the
// workflow has and in what order. Fenced code blocks are skipped so shell
// comments inside examples do not turn into headings.
func skillWorkflowHeadings(content string) []string {
	var (
		out     []string
		used    int
		inFence bool
	)
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			return true
		}
		if inFence {
			return true
		}
		heading, ok := markdownHeadingText(trimmed)
		if !ok {
			return true
		}
		if used+len(heading) > skillSkeletonMaxChars {
			return false
		}
		used += len(heading)
		out = append(out, heading)
		return len(out) < skillSkeletonMaxHeadings
	})
	return out
}

// markdownHeadingText returns the text of an ATX heading line. Six hashes is
// the deepest markdown heading level, and a heading requires a space after the
// hashes, so `#hashtag` and `#######` stay ordinary text. This is a strict
// line-scoped ATX check rather than the compaction summary's heading regex,
// because skill bodies are scanned per request on trimmed content where a
// section-header regex would over-match log and diff markers.
func markdownHeadingText(line string) (string, bool) {
	if line == "" || line[0] != '#' {
		return "", false
	}
	hashes := 0
	for hashes < len(line) && line[hashes] == '#' {
		hashes++
	}
	if hashes > 6 || hashes == len(line) {
		return "", false
	}
	if line[hashes] != ' ' && line[hashes] != '\t' {
		return "", false
	}
	text := strings.TrimSpace(line[hashes:])
	return text, text != ""
}
