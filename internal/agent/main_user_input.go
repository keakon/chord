package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleUserMessage processes a new user message. When the agent is busy
// (turn != nil), the message is queued and processed later when the agent
// becomes idle (with any other queued messages in one batch). When idle,
// slash commands are handled immediately; otherwise a new turn is started.
func (a *MainAgent) handleUserMessage(evt Event) {
	requestID := ""
	inputID := ""
	var content string
	var parts []message.ContentPart
	acceptedOrder := int64(0)
	switch p := evt.Payload.(type) {
	case acceptedUserMessage:
		inputID = p.InputID
		requestID = p.RequestID
		content = p.Content
		parts = p.Parts
		acceptedOrder = p.AcceptedOrder
		a.markRawUserMessageDispatched(acceptedOrder)
	case string:
		content = p
	case []message.ContentPart:
		parts = p
		// Extract text parts for slash-command detection.
		for _, part := range parts {
			if part.Type == message.ContentPartText {
				content += part.Text
			}
		}
	default:
		log.Errorf("handleUserMessage: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}

	log.Debugf("handling user message content_len=%v", len(content))
	if a.sessionExecutionPaused() && a.pause.restored && !IsTUILocalOnlySlashCommand(content) && !IsIdleOnlySlashCommand(content) {
		a.handleDeliveryOperation(&deliveryOperation{inputID: inputID, acceptedOrder: acceptedOrder, submit: &SubmitInput{OperationID: makeRequestID(), RequestID: requestID, Target: a.InputDeliveryTarget(), DeliveryPolicy: recovery.InputDeliveryNextRequest, Content: content, Parts: parts}})
		return
	}
	if a.handlePausedSessionInput(content, parts, inputID, requestID, acceptedOrder) {
		return
	}

	// /export, /models, /tier, and /compact are local-only: never queue or send to the model.
	// Pass busy = (a.turn != nil) so handlers skip setIdleAndDrainPending and
	// don't clobber the active turn while it's mid-retry.
	if a.handleLocalOnlySlashCommands(content, parts, a.turn != nil) {
		a.emitInputResult(requestID, InputHandled, "", 0)
		return
	}
	if a.handleQuestionUserCommand(content, requestID) {
		return
	}
	if !a.cancelCoversAcceptedOrder(acceptedOrder) {
		a.mailboxDeliveryPaused.Store(false)
	}
	if a.questions.paused {
		a.handleQuestionCommand(&questionCommand{operation: QuestionOperation{Operation: questionOpResume, OperationID: makeRequestID()}, ctx: a.parentCtx})
	}
	a.interruptQuestionWaits()

	a.explicitUserTurnCount.Add(1)
	a.sweepSubAgentLifecycle()

	trimmedContent := strings.TrimSpace(content)
	isMCPCommand := trimmedContent == "/mcp" || strings.HasPrefix(trimmedContent, "/mcp ")

	// A turn with no pending work can never consume queued input: settle it so
	// this message starts real work instead of queueing behind the stall.
	a.settleStalledTurn("user message")

	// When busy (turn != nil) or an MCP transition is in flight, queue the message;
	// it will be drained and sent in one batch when idle.
	if a.turn != nil || a.mcpTransitionActive.Load() {
		if a.turn != nil {
			if a.tryHandleBusySlashCommand(content) {
				a.emitInputResult(requestID, InputHandled, "", 0)
				return
			}
		}
		if a.mcpTransitionActive.Load() && isMCPCommand {
			a.emitInputResult(requestID, InputRejected, "MCP change already in progress", 0)
			a.emitToTUI(ToastEvent{Message: "MCP change already in progress", Level: "warn"})
			return
		}
		if !IsIdleOnlySlashCommand(strings.TrimSpace(content)) {
			a.handleDeliveryOperation(&deliveryOperation{userIntentApplied: true, inputID: inputID, acceptedOrder: acceptedOrder, submit: &SubmitInput{OperationID: makeRequestID(), RequestID: requestID, Target: a.InputDeliveryTarget(), DeliveryPolicy: recovery.InputDeliveryNextRequest, Content: content, Parts: parts}})
			return
		}
		a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pendingUserMessage{
			InputID: inputID, RequestID: requestID,
			AcceptedOrder: acceptedOrder,
			Content:       content,
			Parts:         parts,
			FromUser:      true,
		})
		a.emitInputResult(requestID, InputQueued, "", 0)
		return
	}

	// Idle: session and compaction commands before starting a turn.
	if a.tryHandleSlashCommand(content) {
		a.emitInputResult(requestID, InputHandled, "", 0)
		return
	}

	// This message is the user's explicit re-engagement: restored inputs it
	// is queued behind may run now.
	a.releaseRestoredHold()
	if a.delivery.inflight != nil || len(a.delivery.pending) > 0 || a.delivery.persistenceError != nil || a.deliveryHasRunnableQueue() {
		a.handleDeliveryOperation(&deliveryOperation{userIntentApplied: true, inputID: inputID, acceptedOrder: acceptedOrder, submit: &SubmitInput{OperationID: makeRequestID(), RequestID: requestID, Target: a.InputDeliveryTarget(), DeliveryPolicy: recovery.InputDeliveryNextRequest, Content: content, Parts: parts}})
		return
	}
	// A parked queue waits for this action: enqueue the new message behind it and
	// dispatch the whole batch in arrival order instead of starting a turn with
	// only the new message.
	if a.pendingUserDrainSuspended {
		a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pendingUserMessage{
			InputID: inputID, RequestID: requestID,
			AcceptedOrder: acceptedOrder,
			Content:       content,
			Parts:         parts,
			FromUser:      true,
		})
		a.resumePendingUserDrain()
		a.drainPendingUserMessages()
		a.emitInputResult(requestID, InputQueued, "", 0)
		return
	}

	// Start a new turn and call LLM.
	a.tryRecoverPersistenceBeforeTurn()
	a.stageNextSubAgentMailboxBatch()
	a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))
	a.turn.originAcceptedOrder = acceptedOrder
	turnID := a.turn.ID
	turnCtx := a.turn.Ctx

	outC, outP := a.expandSlashCommandForModel(content, parts)
	outC, outP = a.filterUnsupportedParts(outC, outP)
	userMsg := message.Message{
		InputID: inputID,
		Role:    message.RoleUser,
		Content: outC,
		Parts:   outP,
	}
	a.recordCommittedUserMessage(userMsg)
	a.stageSessionInput(pendingUserMessage{InputID: inputID, RequestID: requestID, AcceptedOrder: acceptedOrder, Content: outC, Parts: outP, FromUser: true})

	// A cancel request accepted before this message was dispatched keeps the
	// message but not the work: the turn exists, so the transcript and recovery
	// keep the usual shape of a cancelled turn, and the model is never called.
	if a.cancelCoversAcceptedOrder(acceptedOrder) {
		a.emitInputResult(requestID, InputRejected, "cancelled before model work started", turnID)
		log.Infof("accepted user message cancelled before its first request order=%v turn_id=%v", acceptedOrder, turnID)
		a.handleTurnCancelled(a.abortTurn(a.turn, true))
		return
	}

	a.emitInputResult(requestID, InputStarted, "", turnID)
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
}

func (a *MainAgent) handleAppendContext(evt Event) {
	var msg message.Message
	switch p := evt.Payload.(type) {
	case message.Message:
		msg = p
	case string:
		msg = message.Message{Role: message.RoleUser, Content: p}
	default:
		return
	}
	if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 {
		return
	}
	msg.Role = "user"
	if nativeMessagesTurnPending(a.ctxMgr) {
		msg.Parts = cloneContentParts(msg.Parts)
		a.pendingNativeContextAppends = append(a.pendingNativeContextAppends, msg)
		return
	}
	a.ctxMgr.Append(msg)
	a.recordEvidenceFromMessage(msg)
	if a.recoveryManager() != nil {
		persistMsg := msg
		if strings.TrimSpace(persistMsg.Content) == "" {
			persistMsg.Content = message.UserPromptPlainText(msg)
		}
		a.persistAsync(identity.MainAgentID, persistMsg)
	}
}
