package agent

import (
	"encoding/json"
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
//
// A recorded name this worker's model-facing catalog does not expose — a
// manual-only skill, one the worker's ruleset denies, or one that left the
// catalog — cannot be loaded again by the model: LoadSkill resolves names
// against that same catalog and refuses the rest. Such names stay in the
// record but are listed apart, because a reload hint they cannot satisfy is
// worse than no hint at all.
const (
	subAgentCheckpointSkillsPrefix       = "- Skills loaded earlier: "
	subAgentCheckpointOmittedNamesPrefix = "- Omitted skill names carried: "
	subAgentCheckpointSkillsHint         = " (instructions may have been removed above; call `skill` again when a workflow still applies)"
	subAgentCheckpointNotLoadablePrefix  = "- Skills not loadable by you: "
	subAgentCheckpointNotLoadableHint    = " (cannot be re-loaded by you; the archived history holds their instructions)"
)

// subAgentCheckpointSkillOmittedRe parses the overflow note back, so an
// overflow stays visible across compressions instead of being silently
// forgotten one checkpoint at a time.
var subAgentCheckpointSkillOmittedRe = regexp.MustCompile(`^\(\+(\d+) more\)$`)

// subAgentCheckpointSkills renders the skills line body, or "none" when the
// subagent never loaded one. The reload hint appears only when at least one
// recorded name is still loadable, so the line never invites a `skill` call
// the worker's own catalog would refuse.
func subAgentCheckpointSkills(s *SubAgent, messages []message.Message) string {
	names, omitted, omittedNames := collectSubAgentCheckpointSkillNamesWithOverflow(s, messages)
	if len(names) == 0 {
		return "none"
	}
	notLoadable := subAgentNotLoadableSkillNames(s, names)
	parts := append([]string(nil), names...)
	if omitted > 0 {
		parts = append(parts, fmt.Sprintf("(+%d more)", omitted))
	}
	result := strings.Join(parts, ", ")
	if len(notLoadable) < len(names) {
		result += subAgentCheckpointSkillsHint
	}
	if len(notLoadable) > 0 {
		result += "\n" + subAgentCheckpointNotLoadablePrefix + strings.Join(notLoadable, ", ") + subAgentCheckpointNotLoadableHint
	}
	if len(omittedNames) > 0 {
		encoded, err := json.Marshal(omittedNames)
		if err == nil {
			result += "\n" + subAgentCheckpointOmittedNamesPrefix + string(encoded)
		}
	}
	return result
}

// subAgentNotLoadableSkillNames returns the recorded names this worker's
// model-facing catalog no longer exposes, in input order. Judged by the same
// catalog LoadSkill resolves against, so the two cannot disagree about which
// names a `skill` call can still reach.
func subAgentNotLoadableSkillNames(s *SubAgent, names []string) []string {
	modelLoadable := checkpointModelLoadable(s.visibleSkillsSnapshot())
	var notLoadable []string
	for _, name := range names {
		if !modelLoadable(name) {
			notLoadable = append(notLoadable, name)
		}
	}
	return notLoadable
}

// collectSubAgentCheckpointSkillNamesWithOverflow returns the skills whose instructions
// the subagent currently holds, merged with the names its newest checkpoint
// already recorded, plus the number dropped by the cap. Only the newest
// checkpoint is read: it has itself merged everything older.
func collectSubAgentCheckpointSkillNamesWithOverflow(s *SubAgent, messages []message.Message) ([]string, int, []string) {
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
		names, omitted, omittedNames := parseSubAgentCheckpointSkills(msg.Content)
		for _, name := range names {
			seen[name] = struct{}{}
		}
		for _, name := range omittedNames {
			seen[name] = struct{}{}
		}
		carriedOmitted = max(omitted-len(omittedNames), 0)
		break
	}
	if len(seen) == 0 {
		return nil, carriedOmitted, nil
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	omitted := carriedOmitted
	var omittedNames []string
	if len(names) > checkpointMaxSkillNames {
		omittedNames = append(omittedNames, names[checkpointMaxSkillNames:]...)
		omitted += len(omittedNames)
		names = names[:checkpointMaxSkillNames]
	}
	return names, omitted, omittedNames
}

// parseSubAgentCheckpointSkills lifts the recorded names and the carried
// omission count out of a subagent structured checkpoint. Names are single
// tokens (optionally `plugin:skill`), and the line's trailing prose is not an
// entry, so isCheckpointSkillName rejects anything that is not a name.
func parseSubAgentCheckpointSkills(content string) ([]string, int, []string) {
	var value string
	var carriedNames []string
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(trimmed, subAgentCheckpointSkillsPrefix); ok {
			value = after
			continue
		}
		if strings.HasPrefix(trimmed, subAgentCheckpointOmittedNamesPrefix) {
			var names []string
			if err := json.Unmarshal([]byte(strings.TrimPrefix(trimmed, subAgentCheckpointOmittedNamesPrefix)), &names); err != nil {
				continue
			}
			for _, name := range names {
				if isCheckpointSkillName(name) {
					carriedNames = append(carriedNames, name)
				}
			}
		}
	}
	if value == "" {
		return nil, 0, carriedNames
	}
	value = strings.TrimSuffix(value, subAgentCheckpointSkillsHint)
	if value == "none" {
		return nil, 0, carriedNames
	}
	var names []string
	omitted := 0
	seen := make(map[string]struct{})
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
		if _, dup := seen[token]; dup {
			continue
		}
		seen[token] = struct{}{}
		names = append(names, token)
	}
	dedupedCarriedNames := make([]string, 0, len(carriedNames))
	for _, name := range carriedNames {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		dedupedCarriedNames = append(dedupedCarriedNames, name)
	}
	return names, omitted, dedupedCarriedNames
}
