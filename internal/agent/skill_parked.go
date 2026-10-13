package agent

import (
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

// parkedSkillSnapshot is an immutable, runtime-only projection of the canonical
// task transcript. It retains skill evidence, never tool bodies or attachments.
// Restore rebuilds it while loading history, so UI queries do not perform I/O.
type parkedSkillSnapshot struct {
	checkoutRoot  string
	messages      []message.Message
	loadedEarlier []string
}

func newParkedSkillSnapshot(history []message.Message, root string) *parkedSkillSnapshot {
	snapshot := &parkedSkillSnapshot{checkoutRoot: root}
	snapshot.loadedEarlier, _ = collectCheckpointSkillNames(history)
	skillCalls := make(map[string]bool)
	for _, msg := range history {
		if msg.Role == message.RoleAssistant {
			var calls []message.ToolCall
			for _, call := range msg.ToolCalls {
				switch tools.NormalizeName(call.Name) {
				case tools.NameRead:
					calls = append(calls, message.ToolCall{ID: call.ID, Name: tools.NameRead})
				case tools.NameSkill:
					skillCalls[call.ID] = true
					calls = append(calls, message.ToolCall{ID: call.ID, Name: tools.NameSkill, Args: append([]byte(nil), call.Args...)})
				}
			}
			if len(calls) > 0 {
				snapshot.messages = append(snapshot.messages, message.Message{Role: message.RoleAssistant, ToolCalls: calls})
			}
			continue
		}
		if msg.Role == message.RoleTool && (msg.SkillActivation != nil || skillCalls[msg.ToolCallID]) {
			status := msg.ToolStatus
			if skillCalls[msg.ToolCallID] {
				if !restoreSkillResultSucceeded(msg) {
					continue
				}
				status = message.ToolStatusSuccess
			}
			snapshot.messages = append(snapshot.messages, message.Message{Role: message.RoleTool, ToolCallID: msg.ToolCallID, ToolStatus: status, SkillActivation: msg.SkillActivation.Clone()})
		}
		if msg.SkillDiscovery != nil || msg.CompactionSkillActivation != nil {
			snapshot.messages = append(snapshot.messages, message.Message{Role: msg.Role, Kind: msg.Kind, Content: discoveryNoticeContent(msg), IsCompactionSummary: msg.IsCompactionSummary, SkillDiscovery: msg.SkillDiscovery.Clone(), CompactionSkillActivation: msg.CompactionSkillActivation.Clone()})
		}
	}
	return snapshot
}

func discoveryNoticeContent(msg message.Message) string {
	if msg.SkillDiscovery != nil {
		return msg.Content
	}
	return ""
}

func (s *SubAgent) parkedSkillSnapshot() *parkedSkillSnapshot {
	if s == nil || s.ctxMgr == nil {
		return nil
	}
	root := ""
	if runtime := s.directoryInstructions.Load(); runtime != nil {
		root = runtime.binding.CheckoutRoot
	}
	return newParkedSkillSnapshot(s.ctxMgr.Snapshot(), root)
}

// Only called by the background session loader, never a focused UI accessor.
func restoredParkedSkillSnapshot(history []message.Message, workDir, contentRoot string) *parkedSkillSnapshot {
	if workDir == "" {
		workDir = contentRoot
	}
	workDir, workErr := canonicalInstructionDirectory(workDir)
	contentRoot, contentErr := canonicalInstructionDirectory(contentRoot)
	root := ""
	if workErr == nil && contentErr == nil {
		root, _ = canonicalInstructionDirectory(pathutil.CheckoutRoot(workDir, contentRoot))
	}
	return newParkedSkillSnapshot(history, root)
}

func (a *MainAgent) parkedTaskSkillInvocationStates(task *DurableTaskRecord) []skill.InvocationState {
	catalog, ruleset := a.parkedTaskCatalogAndRuleset(task)
	if catalog == nil {
		return nil
	}
	view := skillActivationView{TaskID: task.TaskID, Catalog: catalog, Ruleset: ruleset}
	var history []message.Message
	earlier := skillNameSet(task.InvokedSkillNames)
	if earlier == nil {
		earlier = make(map[string]struct{})
	}
	if snapshot := task.skillSnapshot; snapshot != nil {
		view.CheckoutRoot = snapshot.checkoutRoot
		history = snapshot.messages
		for _, name := range snapshot.loadedEarlier {
			earlier[name] = struct{}{}
		}
	}
	states := skillInvocationStatesFromHistory(history, view)
	for i := range states {
		if states[i].Meta != nil {
			_, states[i].LoadedEarlier = earlier[states[i].Meta.Name]
		}
	}
	return states
}
