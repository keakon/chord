package agent

import (
	"encoding/json"
	"slices"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// A committed question batch is sufficient to settle a missing Question tool
// result after a crash. Pending decisions return wait_interrupted; they remain
// available through their durable IDs and are never re-created here.
func restoreQuestionToolResults(msgs []message.Message) []message.Message {
	records := map[string]QuestionSnapshot{}
	calls := map[string]message.ToolCall{}
	for _, msg := range msgs {
		for _, call := range msg.ToolCalls {
			if tools.NormalizeName(call.Name) == tools.NameQuestion {
				calls[call.ID] = call
			}
		}
		var fact QuestionFact
		if len(msg.Question) > 0 && json.Unmarshal(msg.Question, &fact) == nil {
			for _, q := range fact.Updates {
				if q.TaskID == identity.MainAgentID {
					records[q.ID] = q
				}
			}
		}
	}
	restored := slices.Clone(msgs)
	for i, msg := range restored {
		call, ok := calls[msg.ToolCallID]
		if !ok || msg.Role != message.RoleTool || msg.ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown {
			continue
		}
		args, err := tools.DecodeQuestionArgs(call.Args)
		if err != nil {
			continue
		}
		ids := slices.Clone(args.WaitFor)
		if len(ids) == 0 {
			var batch []QuestionSnapshot
			for _, q := range records {
				if q.CallID == call.ID {
					batch = append(batch, q)
				}
			}
			slices.SortFunc(batch, func(x, y QuestionSnapshot) int { return x.Index - y.Index })
			for _, q := range batch {
				ids = append(ids, q.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		result := tools.QuestionResult{Status: tools.QuestionStatusResolved}
		known := true
		for _, id := range ids {
			q, ok := records[id]
			if !ok {
				known = false
				break
			}
			if q.pending() {
				result.PendingIDs = append(result.PendingIDs, id)
			} else {
				result.Answers = append(result.Answers, q.answer())
			}
		}
		if !known {
			continue
		}
		if len(result.PendingIDs) > 0 {
			result.Status = tools.QuestionStatusWaitInterrupted
		}
		data, err := json.Marshal(result)
		if err != nil {
			continue
		}
		msg.Content = string(data)
		msg.ToolPayload = string(data)
		msg.ToolStatus = string(ToolResultStatusSuccess)
		msg.ToolRecoveryState = ""
		restored[i] = msg
	}
	return restored
}
