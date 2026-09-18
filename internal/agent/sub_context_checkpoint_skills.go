package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// Subagent checkpoint skill continuity.
//
// A skill's instructions live only in its `skill` tool result, so a context
// compression that drops that result removes them from the live context. The
// checkpoint records the names so a continuation can tell a workflow was in
// effect and re-invoke it.
//
// The names cannot come from the invoked-skill state alone: that state is
// recomputed from the surviving messages after every compression (see
// restoreInvokedSkills) and therefore forgets the skills whose results were
// archived. Keeping them there instead would make the sidebar claim loaded
// instructions that are no longer in context. So the checkpoint merges its
// state-derived names with the names the previous checkpoint recorded — the
// same carry the main agent's skills section performs — and the newest
// checkpoint has itself merged everything older.
const (
	subAgentCheckpointSkillsPrefix = "- Skills loaded earlier: "
	subAgentCheckpointSkillsHint   = " (instructions may have been removed above; call `skill` again when a workflow still applies)"
)

// subAgentCheckpointSkillOmittedRe parses the overflow note back, so an
// overflow stays visible across compressions instead of being silently
// forgotten one checkpoint at a time.
var subAgentCheckpointSkillOmittedRe = regexp.MustCompile(`^\(\+(\d+) more\)$`)

// subAgentCheckpointSkills renders the skills line body, or "none" when the
// subagent never loaded one.
func subAgentCheckpointSkills(s *SubAgent, messages []message.Message) string {
	names, omitted := collectSubAgentCheckpointSkillNames(s, messages)
	if len(names) == 0 {
		return "none"
	}
	parts := append([]string(nil), names...)
	if omitted > 0 {
		parts = append(parts, fmt.Sprintf("(+%d more)", omitted))
	}
	return strings.Join(parts, ", ") + subAgentCheckpointSkillsHint
}

// collectSubAgentCheckpointSkillNames returns the skills whose instructions
// the subagent currently holds, merged with the names its newest checkpoint
// already recorded, plus the number dropped by the cap. Only the newest
// checkpoint is read: it has itself merged everything older.
func collectSubAgentCheckpointSkillNames(s *SubAgent, messages []message.Message) ([]string, int) {
	seen := make(map[string]struct{})
	for _, name := range s.invokedSkillNamesSnapshot() {
		seen[name] = struct{}{}
	}
	carriedOmitted := 0
	for i := len(messages) - 1; i >= 0; i-- {
		msg := &messages[i]
		if msg.Role != message.RoleUser || !msg.IsCompactionSummary || !strings.Contains(msg.Content, subAgentCheckpointSkillsPrefix) {
			continue
		}
		names, omitted := parseSubAgentCheckpointSkillNames(msg.Content)
		for _, name := range names {
			seen[name] = struct{}{}
		}
		carriedOmitted = omitted
		break
	}
	if len(seen) == 0 {
		return nil, carriedOmitted
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	omitted := carriedOmitted
	if len(names) > checkpointMaxSkillNames {
		omitted += len(names) - checkpointMaxSkillNames
		names = names[:checkpointMaxSkillNames]
	}
	return names, omitted
}

// parseSubAgentCheckpointSkillNames lifts the recorded names and the carried
// omission count out of a subagent structured checkpoint. Names are single
// tokens (optionally `plugin:skill`), and the line's trailing prose is not an
// entry, so isCheckpointSkillName rejects anything that is not a name.
func parseSubAgentCheckpointSkillNames(content string) ([]string, int) {
	var value string
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, subAgentCheckpointSkillsPrefix) {
			value = strings.TrimPrefix(trimmed, subAgentCheckpointSkillsPrefix)
			break
		}
	}
	if value == "" {
		return nil, 0
	}
	value = strings.TrimSuffix(value, subAgentCheckpointSkillsHint)
	if value == "none" {
		return nil, 0
	}
	var names []string
	omitted := 0
	for token := range strings.SplitSeq(value, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if match := subAgentCheckpointSkillOmittedRe.FindStringSubmatch(token); match != nil {
			if count, err := strconv.Atoi(match[1]); err == nil {
				omitted += count
			}
			continue
		}
		if !isCheckpointSkillName(token) {
			continue
		}
		names = append(names, token)
	}
	return names, omitted
}
