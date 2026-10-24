package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/notifymcp"
	"github.com/keakon/chord/internal/toolname"
)

const externalNotifyMaxMessage = notifymcp.MaxMessageBytes

// handleExternalMCP serves the private SDK MCP server over Claude Code's
// control channel. The session read loop calls it before dispatching events,
// so a notify never re-enters the worker event loop.
func (w *externalWorker) handleExternalMCP(ctx context.Context, serverName string, message json.RawMessage) json.RawMessage {
	if serverName != notifymcp.ServerName {
		return notifymcp.Unavailable(message, "unknown MCP server")
	}
	if !w.notifyEnabled() {
		return notifymcp.Unavailable(message, "notify is disabled for this role")
	}
	return notifymcp.Handle(ctx, message, w.deliverExternalNotify)
}

// notifyEnabled mirrors the internal worker's Notify visibility. A role that
// disables the tool does not get the MCP surface either.
func (w *externalWorker) notifyEnabled() bool {
	return !w.currentRuleset().IsDisabled(toolname.Notify)
}

// deliverExternalNotify turns a validated tool call into the same durable
// mailbox row an internal Notify produces. The MCP tool call itself is
// preapproved by --allowedTools, so this path is the authorization boundary:
// it refuses calls once the worker is settling and never mutates task state.
func (w *externalWorker) deliverExternalNotify(_ context.Context, request notifymcp.Request) error {
	if w == nil {
		return fmt.Errorf("external worker is unavailable")
	}
	if w.finished.Load() || w.closing.Load() || w.ctx != nil && w.ctx.Err() != nil {
		return fmt.Errorf("external worker is no longer running")
	}
	mailbox, err := w.externalNotifyMailbox(request)
	if err != nil {
		return err
	}
	if w.parent == nil {
		return fmt.Errorf("external worker is unavailable")
	}
	w.parent.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: w.instanceID, Payload: mailbox})
	return nil
}

func (w *externalWorker) externalNotifyMailbox(request notifymcp.Request) (*SubAgentMailboxMessage, error) {
	message := strings.TrimSpace(request.Message)
	if message == "" {
		return nil, fmt.Errorf("message must not be empty")
	}
	if len(message) > externalNotifyMaxMessage {
		return nil, fmt.Errorf("message exceeds %d bytes", externalNotifyMaxMessage)
	}
	kind := strings.TrimSpace(request.Kind)
	if kind == "" {
		kind = "notice"
	}
	priority := SubAgentMailboxPriorityNotify
	subtype := ""
	switch kind {
	case "notice":
	case "decision":
		priority = SubAgentMailboxPriorityInterrupt
		subtype = agentMessageSubtypeDecision
	default:
		return nil, fmt.Errorf("kind must be notice or decision")
	}
	payload := bytes.TrimSpace(request.Payload)
	if len(payload) > 0 && string(payload) != "null" {
		if len(payload) > maxAgentMessagePayloadBytes || payload[0] != '{' {
			return nil, fmt.Errorf("payload must be a JSON object within %d bytes", maxAgentMessagePayloadBytes)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(payload, &object); err != nil || object == nil {
			return nil, fmt.Errorf("payload must be a JSON object within %d bytes", maxAgentMessagePayloadBytes)
		}
	} else {
		payload = nil
	}
	ownerAgentID, ownerTaskID, _, _ := w.OwnerSnapshot()
	return &SubAgentMailboxMessage{
		AgentID:        w.instanceID,
		TaskID:         w.taskID,
		OwnerAgentID:   ownerAgentID,
		OwnerTaskID:    ownerTaskID,
		Kind:           SubAgentMailboxKindNotice,
		MessageType:    AgentMessageTypeNotice,
		Subtype:        subtype,
		Priority:       priority,
		Summary:        message,
		Payload:        message,
		MessagePayload: append(json.RawMessage(nil), payload...),
		RequiresAck:    false,
		// A notify is the worker's own report, never a lifecycle event. The
		// task record may mirror its text but must not flip state on it.
		ReportOnly: true,
	}, nil
}
