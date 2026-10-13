package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

type pausedInputsBackend struct {
	mockBackend
	status agent.SessionPauseStatus
	inputs []recovery.PendingInput
}

func (b *pausedInputsBackend) CurrentSessionPauseStatus() agent.SessionPauseStatus { return b.status }
func (b *pausedInputsBackend) PendingSessionInputs() []recovery.PendingInput       { return b.inputs }

func TestHeadlessPausedInputsProjectionBoundsAndOmitsAttachments(t *testing.T) {
	backend := &pausedInputsBackend{status: agent.SessionPauseStatus{State: agent.SessionPauseReady}}
	for i := range 10 {
		backend.inputs = append(backend.inputs, recovery.PendingInput{InputID: fmt.Sprintf("input-%d", i), OwnerTaskID: "task-1", Content: "  Input\n  preview\ttext  ", Parts: []message.ContentPart{{Type: message.ContentPartImage, ImagePath: "/private/attachment", Data: []byte("private-bytes")}, {Type: message.ContentPartPDF, FileName: "private-document.pdf"}}})
	}
	projection := headlessPausedInputProjection(backend)
	if projection == nil || !projection.Paused || projection.PendingInputCount != 10 || len(projection.Inputs) != 8 {
		t.Fatalf("projection=%+v", projection)
	}
	for _, input := range projection.Inputs {
		if input.OwnerTaskID != "task-1" || input.Preview != "Input preview text" || input.AttachmentCount != 2 {
			t.Fatalf("summary=%+v", input)
		}
	}
	data, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/private/attachment", "private-bytes", "private-document", "parts", "image_path", "data_bytes"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("attachment leaked: %s", data)
		}
	}
	backend.status.State = "running"
	if p := headlessPausedInputProjection(backend); p.Paused || p.PendingInputCount != 10 {
		t.Fatalf("stale checkpoint controls runtime pause: %+v", p)
	}
}

func TestHeadlessInputPreviewUsesUnicodeBoundAndTextParts(t *testing.T) {
	preview := headlessInputPreview(recovery.PendingInput{Content: "\x00\t" + strings.Repeat("文", 200) + "\n"})
	if utf8.RuneCountInString(preview) != 160 || strings.ContainsAny(preview, "\x00\t\n") {
		t.Fatalf("invalid Unicode preview %q", preview)
	}
	preview = headlessInputPreview(recovery.PendingInput{Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "hidden content", DisplayText: "Document summary"}, {Type: message.ContentPartText, Text: "extra text"}, {Type: message.ContentPartImage, ImagePath: "/private/image"}}})
	if preview != "Document summary extra text" {
		t.Fatalf("multipart preview=%q", preview)
	}
}

func TestHeadlessPausedInputProjectionRejectsInvalidIdentity(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129), "input\n1", "input 1"} {
		backend := &pausedInputsBackend{status: agent.SessionPauseStatus{State: agent.SessionPauseReady}, inputs: []recovery.PendingInput{{InputID: id, OwnerTaskID: "main"}}}
		if projection := headlessPausedInputProjection(backend); projection != nil {
			t.Fatalf("invalid identity accepted %q", id)
		}
	}
	backend := &pausedInputsBackend{inputs: []recovery.PendingInput{{InputID: "input-1", OwnerTaskID: "main"}, {InputID: "input-1", OwnerTaskID: "other"}}}
	if headlessPausedInputProjection(backend) != nil {
		t.Fatal("duplicate identity accepted")
	}
	var typedNil *agent.MainAgent
	if headlessPausedInputProjection(typedNil) != nil {
		t.Fatal("typed nil backend accepted")
	}
	var nilFake *pausedInputsBackend
	if headlessPausedInputProjection(nilFake) != nil {
		t.Fatal("typed nil optional backend accepted")
	}
	if headlessPausedInputProjection(&mockBackend{}) != nil {
		t.Fatal("missing backend treated as empty queue")
	}
	empty := headlessPausedInputProjection(&pausedInputsBackend{})
	if empty == nil || empty.Inputs == nil || empty.PendingInputCount != 0 || empty.Paused {
		t.Fatalf("empty live queue=%+v", empty)
	}
}

func TestHeadlessStatusResponseIncludesCurrentPausedInputs(t *testing.T) {
	backend := &pausedInputsBackend{status: agent.SessionPauseStatus{State: agent.SessionPauseReady}, inputs: []recovery.PendingInput{{InputID: "input-1", OwnerTaskID: "main", Content: "Pending constraint"}}}
	out := newTestOut()
	defer out.out.closeUntil(time.After(time.Second))
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, &headlessState{sessionID: "session-1"}, out.writer())
	for _, envelope := range out.drain() {
		if envelope.Type != "status_response" {
			continue
		}
		data, err := json.Marshal(envelope.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			PausedInputs *headlessPausedInputs `json:"paused_inputs"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.PausedInputs == nil || !payload.PausedInputs.Paused || payload.PausedInputs.PendingInputCount != 1 {
			t.Fatalf("payload=%s", data)
		}
		return
	}
	t.Fatal("missing status response")
}
