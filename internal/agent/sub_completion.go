package agent

// finishCompletion is shared by regular and degraded Complete calls, after any
// sibling tools settle. New input invalidates either delivery before checking
// child joins or verification; dropping optional metadata must not bypass it.
func (s *SubAgent) finishCompletion(callID string, result *AgentResult) error {
	if pending := s.takePendingUserMessagesForContinuation(); len(pending) > 0 {
		s.appendCompleteToolResult(callID, "Completion deferred: received new user input before completion.")
		s.clearPendingCompleteIntent()
		s.drainContextAppendsBeforeTurn()
		s.appendPendingUserMessages(pending)
		s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
		return nil
	}
	if outstanding := s.parent.outstandingJoinChildTaskIDs(s.taskID); len(outstanding) > 0 {
		s.appendCompleteToolResult(callID, deferredCompleteResult(len(outstanding)))
		s.setPendingCompleteIntent(result)
		s.enterWaitingDescendant(deferredCompleteResult(len(outstanding)))
		return nil
	}
	if err := s.validateCompletionVerification(result.Envelope); err != nil {
		return err
	}
	s.clearPendingCompleteIntent()
	result = s.enrichCompletionResult(result)
	s.appendCompleteToolResult(callID, result.Summary, result.Envelope.VerificationRecords)
	s.sendEvent(Event{Type: EventAgentDone, Payload: result})
	return nil
}
