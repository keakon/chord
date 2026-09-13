package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// buildSubAgentStructuredCheckpoint renders the deterministic structured
// checkpoint that replaces the removed history prefix inside a SubAgent
// context compression. Every field is sourced from existing authoritative
// state (task description, owner metadata, write scope, the message tail,
// changed-file tracking, runtime state); a field with no
// derivable source is marked "unknown" rather than guessed. The checkpoint
// intentionally does not call any live MainAgent state — it reads only the
// SubAgent's own immutable fields, locked snapshots, and the message list
// passed in.
//
// The layout keeps two stable contract fragments so existing readers keep
// working: the "[system] SubAgent context checkpoint:" prefix and the
// "Full pre-checkpoint history: <ref>." trailer carrying the archive address.
func buildSubAgentStructuredCheckpoint(s *SubAgent, messages []message.Message, removed int, reason, archiveRef string) string {
	var b strings.Builder
	toolMeta := buildToolCallMeta(messages)
	fmt.Fprintf(&b, "[system] SubAgent context checkpoint: %d earlier messages were removed for %s.\n", removed, reason)
	b.WriteString("- Task: ")
	b.WriteString(firstLineOrUnknown(strings.TrimSpace(s.taskDesc), 160))
	b.WriteByte('\n')
	b.WriteString("- Owner: ")
	ownerAgentID, ownerTaskID, _, _ := s.ownerSnapshot()
	ownerLabel := strings.TrimSpace(ownerAgentID) + "/" + strings.TrimSpace(ownerTaskID)
	if ownerLabel == "/" {
		ownerLabel = ""
	}
	b.WriteString(blankToUnknown(ownerLabel))
	b.WriteByte('\n')
	b.WriteString("- Write scope: ")
	b.WriteString(blankToUnknown(s.currentWriteScope().Summary()))
	b.WriteByte('\n')
	b.WriteString("- Latest owner/user instruction: ")
	b.WriteString(latestOwnerInstructionForCheckpoint(messages))
	b.WriteByte('\n')
	b.WriteString("- Files read or changed: ")
	b.WriteString(subAgentCheckpointFiles(s, messages))
	b.WriteByte('\n')
	b.WriteString("- Completed actions: ")
	b.WriteString(subAgentCheckpointActions(messages, toolMeta))
	b.WriteByte('\n')
	b.WriteString("- Skills loaded earlier: ")
	b.WriteString(subAgentCheckpointSkills(s))
	b.WriteByte('\n')
	b.WriteString("- Known failures: ")
	b.WriteString(subAgentCheckpointFailures(messages, toolMeta))
	b.WriteByte('\n')
	b.WriteString("- Open blocker: ")
	b.WriteString(subAgentCheckpointBlocker(s))
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Full pre-checkpoint history: %s.", archiveRef)
	return b.String()
}

// subAgentCheckpointSkills records the skills this subagent loaded, by name.
// A skill's instructions exist only in its tool result, so the compression
// that removes the history prefix can take them out of the context without
// leaving any trace that a workflow was in effect. Names cost one line;
// re-injecting the bodies would spend the context the compression just
// reclaimed. The list comes from the subagent's own invoked-skill state, which
// is recomputed only after this checkpoint is built, so a chain of
// compressions cannot erode it. That state does not track which side of the
// cut each skill landed on, hence the conditional wording — a skill whose
// result survived is still readable above.
func subAgentCheckpointSkills(s *SubAgent) string {
	names := s.invokedSkillNamesSnapshot()
	if len(names) == 0 {
		return "none"
	}
	if len(names) > checkpointMaxSkillNames {
		omitted := len(names) - checkpointMaxSkillNames
		names = append(names[:checkpointMaxSkillNames:checkpointMaxSkillNames], fmt.Sprintf("(+%d more)", omitted))
	}
	return strings.Join(names, ", ") + " (instructions may have been removed above; call `skill` again when a workflow still applies)"
}

func blankToUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

// firstLineOrUnknown returns the first line of value, truncated, or "unknown"
// when the value is empty.
func firstLineOrUnknown(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	if value == "" {
		return "unknown"
	}
	return llm.TruncateStringRunes(value, max, "…")
}

// latestOwnerInstructionForCheckpoint finds the newest message the owner
// actually authored (for a SubAgent, mailbox and Notify inputs both land as
// user messages) and summarizes it, flagging imperative corrections and
// declarative constraints so they survive with their semantics intact.
// Synthetic user-role messages other than mailbox deliveries (compaction
// summaries, pressure notices, loop notices, job results) carry no owner
// instruction and are skipped. The [system] content prefix is checked
// alongside the structured IsUserAuthored marker because checkpoints persisted
// before that marker existed have no other discriminator; it also excludes the
// context checkpoints this function renders. A real owner message that
// happens to open with "[system]" is skipped too — an accepted limitation of
// the legacy format.
func latestOwnerInstructionForCheckpoint(messages []message.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := &messages[i]
		if msg.Role != message.RoleUser || strings.HasPrefix(strings.TrimSpace(msg.Content), "[system]") {
			continue
		}
		if !message.IsUserAuthored(*msg) && msg.Kind != message.KindSubAgentMailbox {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		kind := ""
		switch {
		case looksLikeUserCorrection(text):
			kind = " (correction)"
		case looksLikeStatedConstraint(text):
			kind = " (constraint)"
		}
		return llm.TruncateStringRunes(text, 200, "…") + kind
	}
	return "unknown"
}

// subAgentCheckpointFiles merges the tracked changed files with the
// write/read/delete paths recorded on tool results, deduplicated, bounded.
func subAgentCheckpointFiles(s *SubAgent, messages []message.Message) string {
	seen := make(map[string]struct{})
	var paths []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	s.taskChangesMu.Lock()
	for path := range s.actualChangedFiles {
		add(path)
	}
	s.taskChangesMu.Unlock()
	for i := range messages {
		msg := &messages[i]
		if msg.Role != message.RoleTool || msg.FileState == nil {
			continue
		}
		for _, state := range msg.FileState.Writes {
			add(state.Path)
		}
		for _, state := range msg.FileState.Deletes {
			add(state.Path)
		}
		for _, state := range msg.FileState.Reads {
			add(state.Path)
		}
	}
	if len(paths) == 0 {
		return "unknown"
	}
	sort.Strings(paths)
	if len(paths) > 8 {
		paths = paths[:8]
		paths = append(paths, "…")
	}
	return strings.Join(paths, ", ")
}

// subAgentCheckpointActions summarizes the most recent successful tool
// results (newest first), each as "name: first line", so the model does not
// re-run work the SubAgent already completed. meta is the shared tool-call
// metadata map built once per checkpoint.
func subAgentCheckpointActions(messages []message.Message, meta map[string]toolCallMeta) string {
	var lines []string
	for i := len(messages) - 1; i >= 0 && len(lines) < 3; i-- {
		msg := &messages[i]
		if msg.Role != message.RoleTool || !isToolResultSuccessStatus(msg.ToolStatus) {
			continue
		}
		name := tools.NormalizeName(meta[msg.ToolCallID].Name)
		lines = append(lines, llm.TruncateStringRunes(name+": "+strings.TrimSpace(msg.Content), 120, "…"))
	}
	if len(lines) == 0 {
		return "unknown"
	}
	return strings.Join(lines, " | ")
}

// subAgentCheckpointFailures surfaces the most recent explicit tool failures
// (error status) so the model does not repeat a failed approach after
// compression. meta is the shared tool-call metadata map built once per
// checkpoint.
func subAgentCheckpointFailures(messages []message.Message, meta map[string]toolCallMeta) string {
	var lines []string
	for i := len(messages) - 1; i >= 0 && len(lines) < 2; i-- {
		msg := &messages[i]
		if msg.Role != message.RoleTool || !isToolResultErrorStatus(msg.ToolStatus) {
			continue
		}
		name := tools.NormalizeName(meta[msg.ToolCallID].Name)
		lines = append(lines, llm.TruncateStringRunes(name+": "+strings.TrimSpace(msg.Content), 120, "…"))
	}
	if len(lines) == 0 {
		return "unknown"
	}
	return strings.Join(lines, " | ")
}

// subAgentCheckpointBlocker derives the open blocker from the runtime state:
// a non-running state with a summary is the authoritative "why is this agent
// stuck / waiting" signal.
func subAgentCheckpointBlocker(s *SubAgent) string {
	state, summary := s.runtimeState.snapshot()
	if summary == "" && (state == SubAgentStateRunning || state == SubAgentStateIdle || state == "") {
		return "unknown"
	}
	if summary == "" {
		return string(state)
	}
	return string(state) + ": " + llm.TruncateStringRunes(summary, 160, "…")
}
