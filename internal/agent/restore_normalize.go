package agent

import (
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// normalizeRestoredMessages repairs structural defects that can survive a
// session interruption: trailing assistants stopped mid-stream (interrupted),
// and tool_calls whose matching tool result never got persisted before the
// process exited.
//
// New session writes are canonical and do not rely on this layer — it only
// runs on transcripts loaded from disk on resume. Anything that depends on
// payload content shape (text heuristics, missing ToolStatus fields, etc.)
// belongs at write time, not here.
//
// started is the tool-activity journal lookup keyed by (agent_id, call_id).
// agentID scopes orphan repair to this transcript's journal entries. A nil
// started map means no journal information is available (pre-journal
// session), which falls back to the conservative outcome_unknown default.
func normalizeRestoredMessages(msgs []message.Message, started map[recovery.ToolActivityKey]struct{}, agentID string) []message.Message {
	if len(msgs) == 0 {
		return msgs
	}
	msgs = dropTrailingInterruptedAssistants(msgs)
	if len(msgs) == 0 {
		return msgs
	}
	msgs = dropEmptyAssistantMessages(msgs)
	if len(msgs) == 0 {
		return msgs
	}
	return repairOrphanToolCalls(msgs, started, agentID)
}

func dropTrailingInterruptedAssistants(msgs []message.Message) []message.Message {
	for len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role != "assistant" || last.StopReason != "interrupted" {
			break
		}
		if assistantMessageHasOutput(last) && len(last.ToolCalls) == 0 {
			break
		}
		msgs = msgs[:len(msgs)-1]
	}
	return msgs
}

func dropEmptyAssistantMessages(msgs []message.Message) []message.Message {
	out := msgs[:0]
	for _, msg := range msgs {
		if msg.Role == "assistant" && !assistantMessageHasOutput(msg) {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func assistantMessageHasOutput(msg message.Message) bool {
	if strings.TrimSpace(msg.Content) != "" || len(msg.Parts) > 0 || len(msg.ToolCalls) > 0 || message.HasReplayableThinkingBlocks(msg.ThinkingBlocks) {
		return true
	}
	for _, item := range msg.ResponsesOutput {
		if item.Type == "message" || item.Type == "function_call" {
			return true
		}
	}
	for _, part := range msg.GeminiParts {
		if part.Type == "text" || part.Type == "function_call" {
			return true
		}
	}
	return false
}

// repairOrphanToolCalls walks the transcript and synthesizes an error tool
// message for every assistant tool_call whose matching tool result is missing.
// Without this, sending the loaded history to a provider that requires
// function_call ↔ function_call_output pairing (OpenAI Responses, Anthropic
// tool_use ↔ tool_result) produces an API 400.
//
// The synthetic result's recovery state distinguishes:
//   - not_started: the journal has no started record for this call, so no side
//     effect could have begun (safe to re-check preconditions and retry);
//   - outcome_unknown: the call had started before the interruption, so side
//     effects may be partially or fully applied (verify current state first).
func repairOrphanToolCalls(msgs []message.Message, started map[recovery.ToolActivityKey]struct{}, agentID string) []message.Message {
	agentID = strings.TrimSpace(agentID)
	out := make([]message.Message, 0, len(msgs))
	pending := make(map[string]struct{})
	pendingOrder := make([]string, 0)
	notStartedCount := 0
	outcomeUnknownCount := 0
	flushPending := func() {
		for _, id := range pendingOrder {
			if _, ok := pending[id]; !ok {
				continue
			}
			state := toolRecoveryStateForOrphan(started, agentID, id)
			if state == message.ToolRecoveryStateNotStarted {
				notStartedCount++
			} else {
				outcomeUnknownCount++
			}
			out = append(out, syntheticInterruptedToolResult(id, state))
			delete(pending, id)
		}
		pendingOrder = pendingOrder[:0]
	}

	for _, msg := range msgs {
		if msg.Role != "tool" && len(pending) > 0 {
			flushPending()
		}

		switch msg.Role {
		case "assistant":
			out = append(out, msg)
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" {
					continue
				}
				if _, exists := pending[tc.ID]; exists {
					continue
				}
				pending[tc.ID] = struct{}{}
				pendingOrder = append(pendingOrder, tc.ID)
			}

		case "tool":
			if msg.ToolCallID == "" {
				continue
			}
			if _, ok := pending[msg.ToolCallID]; !ok {
				continue
			}
			delete(pending, msg.ToolCallID)
			out = append(out, msg)

		default:
			out = append(out, msg)
		}
	}

	if len(pending) > 0 {
		flushPending()
	}
	if notStartedCount > 0 || outcomeUnknownCount > 0 {
		log.Infof("restore repaired orphan tool calls agent=%v not_started=%d outcome_unknown=%d", agentID, notStartedCount, outcomeUnknownCount)
	}
	return out
}

// toolRecoveryStateForOrphan classifies one orphan tool call. A missing
// journal (nil started) has no classification information and stays
// conservative: outcome_unknown. An existing journal with no started entry
// proves the tool never reached its execution body.
func toolRecoveryStateForOrphan(started map[recovery.ToolActivityKey]struct{}, agentID, callID string) string {
	if started == nil {
		return message.ToolRecoveryStateOutcomeUnknown
	}
	if _, ok := started[recovery.ToolActivityKey{AgentID: agentID, CallID: callID}]; ok {
		return message.ToolRecoveryStateOutcomeUnknown
	}
	return message.ToolRecoveryStateNotStarted
}

const (
	errRestoreToolResultNotStarted     = "session restored before the tool started; no result was produced. Re-check preconditions before retrying."
	errRestoreToolResultOutcomeUnknown = "session restored after the tool started but before its result was persisted. Its side effects may be partially or fully applied — verify the current state before retrying."
)

func syntheticInterruptedToolResult(callID, recoveryState string) message.Message {
	content := errRestoreToolResultOutcomeUnknown
	if recoveryState == message.ToolRecoveryStateNotStarted {
		content = errRestoreToolResultNotStarted
	}
	return message.Message{
		Role:              "tool",
		ToolCallID:        callID,
		Content:           content,
		ToolStatus:        string(ToolResultStatusError),
		ToolRecoveryState: recoveryState,
	}
}
