package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/takeover"
)

func TestTakeoverInvalidRequestProducesOnlyOneJSONStatus(t *testing.T) {
	cmd := newTakeoverCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"start", "--session", "../sample", "--transfer-id", "transfer-1", "--json"})
	if err := cmd.ExecuteContext(t.Context()); err == nil {
		t.Fatal("invalid session admitted")
	}
	var status takeover.Status
	d := json.NewDecoder(&out)
	if err := d.Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.ErrorCode != takeover.CodeInvalidRequest {
		t.Fatalf("status=%+v", status)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		t.Fatal("stdout contains extra data")
	}
}

func TestHeadlessTransferACKRequiresCurrentIdentity(t *testing.T) {
	id := recovery.TransferIdentity{TransferID: "transfer-1", SessionID: "session-1", SourceRuntimeID: "runtime-1", SourceSessionEpoch: 1, Workspace: "/sample", TargetKind: "tui", TargetInstanceID: "target-1"}
	adapter := &headlessTransferAdapter{pending: &id, reply: make(chan error, 1), binding: "binding-1"}
	ack := transferPreparedAck{TransferID: id.TransferID, SessionID: id.SessionID, SourceRuntimeID: id.SourceRuntimeID, SourceSessionEpoch: id.SourceSessionEpoch, BindingInstanceID: "binding-2", Accepted: true}
	if err := adapter.ack(ack); err == nil {
		t.Fatal("foreign binding ACK admitted")
	}
	select {
	case <-adapter.reply:
		t.Fatal("foreign ACK replied")
	default:
	}
	ack.BindingInstanceID = "binding-1"
	if err := adapter.ack(ack); err != nil {
		t.Fatal(err)
	}
	if err := <-adapter.reply; err != nil {
		t.Fatal(err)
	}
	adapter.pending = nil
	if err := adapter.ack(ack); err == nil {
		t.Fatal("late ACK admitted")
	}
}

func TestHeadlessTransferACKRejectionPreservesReason(t *testing.T) {
	id := recovery.TransferIdentity{TransferID: "transfer-1"}
	adapter := &headlessTransferAdapter{pending: &id, reply: make(chan error, 1), binding: "binding-1"}
	ack := transferPreparedAck{TransferIdentity: id, BindingInstanceID: "binding-1", ErrorCode: takeover.CodePersistenceFailed, Message: "state write failed"}
	if err := adapter.ack(ack); err != nil {
		t.Fatal(err)
	}
	if err := <-adapter.reply; err == nil || !strings.Contains(err.Error(), takeover.CodePersistenceFailed) {
		t.Fatal(err)
	}
}

func TestHeadlessTransferOutputBypassesSubscriptionAndUsesPublicDTO(t *testing.T) {
	s := recovery.SessionPauseStatus{TransferID: "transfer-1", State: recovery.SessionPauseSaved, PendingInputCount: 2, Error: "sample detail", Blockers: []recovery.SessionPauseBlocker{{Kind: "tool", Description: "sample"}}}
	env := transferEnvelope(s, "binding-1")
	if env.Type != headlessTransferSaved {
		t.Fatal(env.Type)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"pending_inputs":2`)) || !bytes.Contains(raw, []byte(`"type":"tool"`)) || !bytes.Contains(raw, []byte(`"binding_instance_id":"binding-1"`)) {
		t.Fatal(string(raw))
	}
}

func TestStdoutFlushWaitsForActualWrite(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	writer := &takeoverBarrierWriter{entered: entered, release: release}
	out := newStdoutWriter(context.Background(), writer)
	go out.run()
	if !out.emit(headlessEnvelope{Type: headlessTransferSaved}) {
		t.Fatal("emit failed")
	}
	<-entered
	done := make(chan bool, 1)
	go func() { done <- out.flush(t.Context()) }()
	select {
	case <-done:
		t.Fatal("flush completed before write")
	default:
	}
	close(release)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("flush failed")
		}
	case <-time.After(time.Second):
		t.Fatal("flush blocked")
	}
	if !out.closeUntil(time.After(time.Second)) {
		t.Fatal("writer close blocked")
	}
}

type takeoverBarrierWriter struct{ entered, release chan struct{} }

func (w *takeoverBarrierWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(p), nil
}

func TestTakeoverExitClassification(t *testing.T) {
	err := cliExitError{code: takeoverExitCode}
	if cliExitCode(err) != 20 || shouldPrintCLIError(err) {
		t.Fatal("transfer exit misclassified")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("transfer exit classified as cancellation")
	}
}
