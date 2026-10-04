package agent

const (
	InputHandled  = "handled"
	InputQueued   = "queued"
	InputStarted  = "started"
	InputRejected = "rejected"
)

// InputResultEvent reports consumption of one input, not completion of its
// model work or durable delivery across a disconnected control-plane client.
type InputResultEvent struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	TurnID    uint64 `json:"turn_id,omitempty"`
	Message   string `json:"message,omitempty"`
}

func (InputResultEvent) agentEvent() {}

// SendUserMessageWithReceipt enqueues a main-agent input without waiting for
// consumption. False means shutdown prevented admission.
func (a *MainAgent) SendUserMessageWithReceipt(content, requestID string) bool {
	p := a.acceptRawUserMessage(content, nil)
	p.RequestID = requestID
	return a.sendEvent(Event{Type: EventUserMessage, Payload: p})
}

func (a *MainAgent) emitInputResult(requestID, status, detail string, turnID uint64) {
	if requestID == "" {
		return
	}
	a.emitToTUI(InputResultEvent{RequestID: requestID, Status: status, Message: detail, TurnID: turnID})
}
