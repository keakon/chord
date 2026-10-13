package agent

import (
	"fmt"
	"strings"
)

func (a *MainAgent) handleInputQueueSlash(content string) bool {
	fields := strings.Fields(content)
	if len(fields) == 0 || fields[0] != "/queue" {
		return false
	}
	if len(fields) != 2 || fields[1] != InputQueuePause && fields[1] != InputQueueResume && fields[1] != InputQueueClear {
		a.emitToTUI(ToastEvent{Message: "Use /queue pause, /queue resume or /queue clear; query status for the input queue", Level: "info"})
		return true
	}
	revision := uint64(0)
	if a.delivery.committed != nil {
		revision = a.delivery.committed.Revision
	}
	op := &deliveryOperation{control: &QueueControl{OperationID: makeRequestID(), Target: a.InputDeliveryTarget(), Action: fields[1], ExpectedQueueRevision: revision}}
	op.failed = func(err error) { a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Input queue: %v", err), Level: "warn"}) }
	a.handleDeliveryOperation(op)
	return true
}
