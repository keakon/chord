package agent

import (
	"fmt"
	"strings"
)

// Explicit commands associate a user change with exact question IDs. Ambiguous
// conversational changes leave dependencies intact for a clarification.
func (a *MainAgent) handleQuestionUserCommand(content, inputID string) bool {
	text := strings.TrimSpace(content)
	var op QuestionOperation
	if after, ok := strings.CutPrefix(text, "/new-task "); ok {
		op = QuestionOperation{Operation: QuestionOpNewTask, UserText: strings.TrimSpace(after)}
	} else if strings.HasPrefix(text, "/question ") {
		fields := strings.Fields(text)
		if len(fields) == 2 && fields[1] == "cancel-task" {
			op = QuestionOperation{Operation: QuestionOpCancelTask, UserText: content}
		}
		if len(fields) == 3 && fields[1] == "withdraw" {
			op = QuestionOperation{Operation: QuestionOpWithdraw, QuestionID: fields[2], UserText: content}
		}
		if len(fields) == 4 && fields[1] == "replace" {
			op = QuestionOperation{Operation: QuestionOpReplace, QuestionID: fields[2], ReplacementID: fields[3], UserText: content}
		}
		if op.Operation == "" {
			a.emitToTUI(ToastEvent{Message: "Use /question withdraw <id> or /question replace <old-id> <new-id>", Level: "warn"})
			a.emitInputResult(inputID, InputRejected, "invalid question command", 0)
			return true
		}
	} else {
		return false
	}
	op.OperationID = makeRequestID()
	if inputID != "" {
		op.OperationID = "user-question:" + inputID
	}
	a.handleQuestionCommand(&questionCommand{operation: op, ctx: a.parentCtx, onReply: func(receipt QuestionReceipt) {
		if receipt.Accepted {
			a.emitInputResult(inputID, InputHandled, "", 0)
		} else {
			a.emitInputResult(inputID, InputRejected, receipt.Error, 0)
			a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Question change rejected: %s", receipt.Error), Level: "warn"})
		}
	}})
	return true
}
