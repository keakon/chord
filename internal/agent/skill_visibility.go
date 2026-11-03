package agent

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// skillBodyVisibleInMessages reports whether a complete skill result with the
// given body digest is still inside the current turn — the window a reload
// would duplicate. The scan stops at the latest user-authored message or
// compaction checkpoint: a body from an earlier turn belongs to work the user
// has already moved past, and a checkpoint replaced the head that held it.
//
// The caller supplies the request-batch gate that requesttrim will apply on
// the next request. A full skill result survives reduction while its age stays
// below the stale age (the only gate a large skill body can reach, because the
// skill summary needs Age >= StaleAgeTurns and MinToolResultsPrune), so a body
// that would already be summarized on the next request must not be reported
// visible. Zero batches disable the age gate for agents without request-level
// reduction (SubAgent histories).
func skillBodyVisibleInMessages(messages []message.Message, digest string, currentBatch uint64, staleAge int) bool {
	if strings.TrimSpace(digest) == "" {
		return false
	}
	for _, body := range visibleSkillBodiesInMessages(messages, currentBatch, staleAge) {
		if body.digest == digest {
			return true
		}
	}
	return false
}

// skillBodyNamesVisibleInMessages returns the skills whose complete encoded
// bodies survive the same turn, checkpoint, and request-reduction gates as
// skillBodyVisibleInMessages. Callers that classify several skills pay for one
// transcript scan instead of one scan per digest.
func skillBodyNamesVisibleInMessages(messages []message.Message, currentBatch uint64, staleAge int) map[string]struct{} {
	visible := make(map[string]struct{})
	for _, body := range visibleSkillBodiesInMessages(messages, currentBatch, staleAge) {
		if body.name != "" {
			visible[body.name] = struct{}{}
		}
	}
	return visible
}

type visibleSkillBody struct {
	name   string
	digest string
}

func visibleSkillBodiesInMessages(messages []message.Message, currentBatch uint64, staleAge int) []visibleSkillBody {
	if len(messages) == 0 {
		return nil
	}
	start := 0
	for i := range slices.Backward(messages) {
		if messages[i].IsCompactionSummary || message.IsUserAuthored(messages[i]) {
			start = i + 1
			break
		}
	}
	type skillCall struct {
		toolName  string
		skillName string
		batch     uint64
	}
	calls := make(map[string]skillCall)
	for i := start; i < len(messages); i++ {
		msg := &messages[i]
		if msg.Role != message.RoleAssistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if strings.TrimSpace(tc.ID) == "" {
				continue
			}
			toolName := tools.NormalizeName(tc.Name)
			skillName := ""
			if toolName == tools.NameSkill {
				skillName = strings.TrimSpace(extractToolArgument(tc.Name, tc.Args))
			}
			calls[tc.ID] = skillCall{toolName: toolName, skillName: skillName, batch: msg.RequestBatch}
		}
	}
	var visible []visibleSkillBody
	for i := start; i < len(messages); i++ {
		msg := &messages[i]
		if msg.Role != message.RoleTool || strings.TrimSpace(msg.ToolCallID) == "" {
			continue
		}
		call, ok := calls[msg.ToolCallID]
		if !ok || call.toolName != tools.NameSkill || !restoreSkillResultSucceeded(*msg) {
			continue
		}
		resultDigest, ok := tools.SkillResultBodyDigest(msg.Content)
		if !ok {
			continue
		}
		if currentBatch > 0 && call.batch > 0 && staleAge > 0 && currentBatch >= call.batch {
			if int(currentBatch-call.batch)+1 >= staleAge {
				continue
			}
		}
		visible = append(visible, visibleSkillBody{name: call.skillName, digest: resultDigest})
	}
	return visible
}
