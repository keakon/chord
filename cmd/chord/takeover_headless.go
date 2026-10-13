package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

var (
	flagGatewayManaged    bool
	flagGatewayInstance   string
	flagGatewayTransferID string
)

const (
	headlessTransferPreparing = "session_transfer_preparing"
	headlessTransferPrepared  = "session_transfer_prepared"
	headlessTransferSaved     = "session_transfer_saved"
	headlessTransferReleased  = "session_transfer_released"
)

type transferPreparedAck struct {
	Type string `json:"type"`
	recovery.TransferIdentity
	BindingInstanceID string `json:"binding_instance_id"`
	Accepted          bool   `json:"accepted"`
	ErrorCode         string `json:"error_code,omitempty"`
	Message           string `json:"message,omitempty"`
}

type headlessTransferAdapter struct {
	mu      sync.Mutex
	pending *recovery.TransferIdentity
	reply   chan error
	out     *stdoutWriter
	binding string
	ready   <-chan struct{}
}

func (h *headlessTransferAdapter) prepare(ctx context.Context, req agent.SessionPauseRequest) error {
	if h.ready != nil {
		select {
		case <-h.ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.mu.Lock()
	id := req.Identity
	h.pending = &id
	h.reply = make(chan error, 1)
	reply := h.reply
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.pending = nil; h.reply = nil; h.mu.Unlock() }()
	status := takeover.PublicStatus(recovery.SessionPauseStatus{TransferIdentity: id, State: recovery.SessionPausePreparing})
	status.BindingInstanceID = h.binding
	if !h.out.emitWithContext(ctx, headlessEnvelope{Type: headlessTransferPreparing, Payload: status}) {
		return fmt.Errorf("target_not_ready: preparing output unavailable")
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-reply:
		return err
	case <-timer.C:
		return fmt.Errorf("target_not_ready: gateway preparation ACK timed out")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *headlessTransferAdapter) ack(ack transferPreparedAck) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending == nil || (ack.TransferID != h.pending.TransferID || ack.SessionID != h.pending.SessionID || ack.SourceRuntimeID != h.pending.SourceRuntimeID || ack.SourceSessionEpoch != h.pending.SourceSessionEpoch) || ack.BindingInstanceID != h.binding {
		return fmt.Errorf("stale_source: preparation ACK identity mismatch")
	}
	var err error
	if !ack.Accepted {
		err = fmt.Errorf("%s: %s", ack.ErrorCode, ack.Message)
	}
	select {
	case h.reply <- err:
		return nil
	default:
		return fmt.Errorf("transfer_busy: duplicate preparation ACK")
	}
}

func transferEnvelope(status recovery.SessionPauseStatus, binding string) *headlessEnvelope {
	kind := "session_transfer_status"
	switch status.State {
	case recovery.SessionPauseSaved:
		kind = headlessTransferSaved
	case recovery.SessionPauseReleased:
		kind = headlessTransferReleased
	}
	payload := takeover.PublicStatus(status)
	payload.BindingInstanceID = binding
	return &headlessEnvelope{Type: kind, Payload: payload}
}

type stdoutBarrier struct{ done chan struct{} }

func (w *stdoutWriter) flush(ctx context.Context) bool {
	barrier := stdoutBarrier{done: make(chan struct{})}
	if !w.emitWithContext(ctx, barrier) {
		return false
	}
	select {
	case <-barrier.done:
		return true
	case <-ctx.Done():
		return false
	case <-w.ctx.Done():
		return false
	}
}
