package agent

import "github.com/keakon/chord/internal/message"

// insertBoundaryMessages places claimed transcript rows before the transient
// tail overlays so the durable request order stays user input then overlays.
func insertBoundaryMessages(messages, inputMessages []message.Message, tailOverlayCount int) []message.Message {
	if len(inputMessages) == 0 {
		return messages
	}
	tail := min(max(tailOverlayCount, 0), len(messages))
	at := len(messages) - tail
	merged := make([]message.Message, 0, len(messages)+len(inputMessages))
	merged = append(merged, messages[:at]...)
	merged = append(merged, inputMessages...)
	return append(merged, messages[at:]...)
}

// mergeBoundaryRequestInputs extends a request snapshot at a request boundary
// (retry round or fallback target) with everything deliverable at that point:
// claimed next-request inputs, pending user messages, native context
// appends, and mailbox arrivals such as JOB RESULT. The order matches the turn
// continuation path: user input first, then mailbox rows, all before the
// transient tail overlays. It must run on the event loop after the claimed
// inputs are durable.
func (a *MainAgent) mergeBoundaryRequestInputs(messages, inputMessages []message.Message, tailOverlayCount int) []message.Message {
	if len(inputMessages) > 0 {
		messages = insertBoundaryMessages(messages, inputMessages, tailOverlayCount)
		a.stageNextSubAgentMailboxBatch()
	}
	messages = a.consumePendingUserMessagesForRequest(messages, tailOverlayCount)
	return a.injectPendingMailboxMessagesForRequest(messages, tailOverlayCount)
}

// rebuildMainLLMRequestMessages reapplies the request-only projections and
// admission pass after a boundary merged new transcript rows.
func (a *MainAgent) rebuildMainLLMRequestMessages(messages []message.Message) ([]message.Message, error) {
	messages = projectSkillDiscoveryMessages(messages, a.skillActivationView())
	messages, err := projectDirectoryInstructionHistory(messages, a.directoryInstructions.Load())
	if err != nil {
		return nil, err
	}
	return a.prepareMessagesForLLMWithOptions(messages, false), nil
}
