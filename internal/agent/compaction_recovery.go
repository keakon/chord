package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// Runtime-owned recovery state.
//
// The runtime, not the summarizer, owns the continuation state a checkpoint
// must not lose: the authoritative latest user request (a Done rejected reason
// included) and the tool calls whose results never reached the transcript.
// Both compaction paths compose it through the same step,
// applyCompactionRecoveryState, so the model summary and every fallback
// (structured fallback and truncate-only) carry the same runtime-owned
// guarantees instead of depending on what the summarizer happened to write.
//
// The rest of the continuation state is already runtime-owned and shared by
// both paths: the complete todo snapshot (ensureCompactionTodoSnapshot), the
// delegated-worker and background-job snapshots, the invoked-skill list, the
// key-file list with its recorded revisions, and the session anchors. This
// file adds only what no other carrier guaranteed.

const (
	// checkpointCurrentUserRequestHeading is the summary section whose body the
	// runtime resolves authoritatively, and the heading a continuation inherits
	// its latest request from.
	checkpointCurrentUserRequestHeading = "## Current User Request"
	// runtimeRecoveryStateHeading is the runtime-owned section for state that
	// has no summary counterpart. It is rendered only when such state exists.
	runtimeRecoveryStateHeading = "## Runtime Recovery State"
	// runtimeRecoveryUnsettledMaxItems bounds the rendered rows; the archived
	// history keeps the complete list.
	runtimeRecoveryUnsettledMaxItems = 10
	// unsettledToolNameFallback names a call whose declaring tool name is not
	// in the archived head.
	unsettledToolNameFallback = "(unknown tool)"
)

// compactionRecoveryState is the runtime-owned slice of the continuation state
// rendered into every checkpoint.
type compactionRecoveryState struct {
	anchor    fallbackAnchor
	unsettled []unsettledToolCall
}

// unsettledToolCall is a tool call whose result never reached the transcript:
// the call had started when the session was interrupted, so its side effects
// may already be partially or fully applied.
type unsettledToolCall struct {
	CallID string
	Tool   string
}

// buildCompactionRecoveryState captures the runtime-owned state for a
// checkpoint. snapshot is the whole transcript, because the latest request can
// live in the preserved tail rather than in the archived head; archivedHead is
// the part this checkpoint replaces.
func buildCompactionRecoveryState(snapshot, archivedHead []message.Message) compactionRecoveryState {
	return buildCompactionRecoveryStateWithAnchor(resolveLatestUserRequestAnchor(snapshot), archivedHead)
}

// buildCompactionRecoveryStateWithAnchor is the anchor-parameterized form: a
// caller that already resolved the latest request reuses it instead of
// scanning the transcript a second time in the same compaction.
func buildCompactionRecoveryStateWithAnchor(anchor fallbackAnchor, archivedHead []message.Message) compactionRecoveryState {
	return compactionRecoveryState{
		anchor:    anchor,
		unsettled: collectUnsettledToolCalls(archivedHead),
	}
}

// collectUnsettledToolCalls lists the archived head's outcome_unknown tool
// results: the synthetic results session restore writes for a call that had
// started before an interruption. The marker exists only in the transcript, so
// a checkpoint that archives it without the caution would let the continuation
// read the call as an ordinary error and retry a mutation whose side effects
// may already be there.
//
// The transcript is append-ordered (every assistant call precedes its tool
// result), so one in-order pass suffices: call names seen so far resolve the
// unsettled results that follow them.
func collectUnsettledToolCalls(archivedHead []message.Message) []unsettledToolCall {
	names := make(map[string]string)
	var calls []unsettledToolCall
	for _, msg := range archivedHead {
		for _, call := range msg.ToolCalls {
			if call.ID == "" {
				continue
			}
			if _, ok := names[call.ID]; !ok {
				names[call.ID] = oneLineCheckpointText(call.Name)
			}
		}
		if msg.Role != message.RoleTool || msg.ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown {
			continue
		}
		name := names[msg.ToolCallID]
		if name == "" {
			name = unsettledToolNameFallback
		}
		calls = append(calls, unsettledToolCall{CallID: oneLineCheckpointText(msg.ToolCallID), Tool: name})
	}
	return calls
}

// applyCompactionRecoveryState composes the runtime-owned state into a
// checkpoint summary body. It has to run before the prior-checkpoint carry is
// appended, so the carry stays the checkpoint's final section.
func applyCompactionRecoveryState(summary string, state compactionRecoveryState) string {
	summary = ensureCompactionLatestRequestAnchor(summary, state.anchor)
	return ensureCompactionUnsettledToolState(summary, state.unsettled)
}

// ensureCompactionLatestRequestAnchor replaces the `## Current User Request`
// section body with the runtime-resolved latest request (including a Done
// rejected reason) and inserts the section when the summary has none. What the
// summarizer wrote for that section is replaced, exactly as the runtime todo
// snapshot replaces the model's classification, so the authoritative request
// never depends on the model restating it.
func ensureCompactionLatestRequestAnchor(summary string, anchor fallbackAnchor) string {
	if strings.TrimSpace(anchor.Text) == "" {
		return summary
	}
	section := modelDrivenCurrentUserRequestSection(anchor)
	start, end, ok := markdownSectionBounds(summary, checkpointCurrentUserRequestHeading)
	if !ok {
		if strings.TrimSpace(summary) == "" {
			return checkpointCurrentUserRequestHeading + "\n" + section
		}
		return checkpointCurrentUserRequestHeading + "\n" + section + "\n\n" + strings.TrimSpace(summary)
	}
	out := strings.TrimRight(summary[:start], "\n") + "\n" + section
	if tail := strings.TrimLeft(summary[end:], "\n"); tail != "" {
		out += "\n\n" + tail
	}
	return out
}

// ensureCompactionUnsettledToolState replaces the runtime recovery section
// with one rendered from the current capture, and removes it entirely when
// nothing is unsettled: a stale block would read as the live state after the
// reset, the same rule the background-job snapshot follows.
func ensureCompactionUnsettledToolState(summary string, calls []unsettledToolCall) string {
	summary = stripRuntimeRecoveryStateSection(summary)
	if len(calls) == 0 {
		return summary
	}
	section := renderRuntimeRecoveryStateSection(calls)
	if summary == "" {
		return section
	}
	return summary + "\n\n" + section
}

// renderRuntimeRecoveryStateSection renders one row per unsettled call.
func renderRuntimeRecoveryStateSection(calls []unsettledToolCall) string {
	var sb strings.Builder
	sb.WriteString(runtimeRecoveryStateHeading)
	sb.WriteString("\nRuntime-owned, rendered from the persisted transcript rather than the summary above; where the two disagree, this section wins.\n")
	sb.WriteString("- Unsettled tool calls (the call started before an interruption and its result was never persisted, so side effects may be partially or fully applied — verify the current state before retrying):\n")
	rendered := 0
	for _, call := range calls {
		if rendered >= runtimeRecoveryUnsettledMaxItems {
			break
		}
		sb.WriteString("  - ")
		sb.WriteString(call.Tool)
		if call.CallID != "" {
			fmt.Fprintf(&sb, " (%s)", call.CallID)
		}
		sb.WriteByte('\n')
		rendered++
	}
	if omitted := len(calls) - rendered; omitted > 0 {
		fmt.Fprintf(&sb, "  - (%d more unsettled tool calls not shown; read the archived history for the complete list)\n", omitted)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// stripRuntimeRecoveryStateSection removes the runtime recovery section, so
// carrying a prior checkpoint forward (or re-ensuring the section) cannot
// leave two copies, one of them stale.
func stripRuntimeRecoveryStateSection(body string) string {
	idx := strings.Index(body, runtimeRecoveryStateHeading)
	if idx < 0 {
		return strings.TrimSpace(body)
	}
	end := len(body)
	if loc := compactionMarkdownHeadingLineRe.FindStringIndex(body[idx+len(runtimeRecoveryStateHeading):]); loc != nil {
		end = idx + len(runtimeRecoveryStateHeading) + loc[0]
	}
	prefix := strings.TrimSpace(body[:idx])
	suffix := strings.TrimSpace(body[end:])
	switch {
	case prefix == "":
		return suffix
	case suffix == "":
		return prefix
	default:
		return prefix + "\n\n" + suffix
	}
}

// oneLineCheckpointText flattens a transcript-authored field onto one line. A
// tool name or call id containing a newline could otherwise forge a section
// heading that every checkpoint reader keys on.
func oneLineCheckpointText(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r", "")
	return strings.ReplaceAll(s, "\n", " ")
}
