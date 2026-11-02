package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestRecordCommittedUserMessageEmitsNormalizedAttachmentPaths(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	payload := []byte("inline attachment bytes")
	a.recordCommittedUserMessage(message.Message{
		Role: message.RoleUser,
		Parts: []message.ContentPart{{
			Type:     message.ContentPartImage,
			MimeType: "image/png",
			Data:     payload,
		}},
	})

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case raw := <-a.Events():
			evt, ok := raw.(UserMessageCommittedEvent)
			if !ok {
				continue
			}
			if evt.MessageIndex < 0 {
				t.Fatalf("MessageIndex = %d, want a durable index", evt.MessageIndex)
			}
			if len(evt.Parts) != 1 {
				t.Fatalf("event parts = %d, want 1", len(evt.Parts))
			}
			part := evt.Parts[0]
			if len(part.Data) != 0 {
				t.Errorf("event kept %d inline bytes, want cleared", len(part.Data))
			}
			if part.ImagePath == "" {
				t.Error("event ImagePath is empty, want the session-owned file")
			}
			if part.DataBytes != int64(len(payload)) {
				t.Errorf("event DataBytes = %d, want %d", part.DataBytes, len(payload))
			}
			return
		case <-deadline.C:
			t.Fatal("timed out waiting for UserMessageCommittedEvent")
		}
	}
}

func TestConsumePendingUserMessagesForRequestNormalizesAttachments(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// The test model does not declare image input; drop the capability filter so
	// the queued attachment reaches the commit path unchanged.
	a.llmMu.Lock()
	a.llmClient = nil
	a.llmMu.Unlock()
	payload := []byte("queued attachment bytes")
	a.pendingUserMessages = []pendingUserMessage{{
		FromUser: true, RequestID: "request-1",
		Parts: []message.ContentPart{{
			Type:     message.ContentPartImage,
			MimeType: "image/png",
			Data:     payload,
		}},
	}}

	request := a.consumePendingUserMessagesForRequest(nil, 0)
	if len(request) != 1 {
		t.Fatalf("request messages = %d, want 1", len(request))
	}
	if request[0].InputRequestID != "request-1" {
		t.Fatalf("InputRequestID = %q, want request-1", request[0].InputRequestID)
	}
	if len(request[0].Parts) != 1 {
		t.Fatalf("request parts = %d, want 1", len(request[0].Parts))
	}
	part := request[0].Parts[0]
	if len(part.Data) != 0 {
		t.Errorf("request kept %d inline bytes, want cleared", len(part.Data))
	}
	if part.ImagePath == "" {
		t.Error("request ImagePath is empty, want the session-owned file")
	}
	if part.DataBytes != int64(len(payload)) {
		t.Errorf("request DataBytes = %d, want %d", part.DataBytes, len(payload))
	}

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case raw := <-a.Events():
			evt, ok := raw.(UserMessageCommittedEvent)
			if !ok {
				continue
			}
			if evt.RequestID != "request-1" {
				t.Fatalf("RequestID = %q, want request-1", evt.RequestID)
			}
			if len(evt.Parts) != 1 {
				t.Fatalf("event parts = %d, want 1", len(evt.Parts))
			}
			if len(evt.Parts[0].Data) != 0 {
				t.Errorf("event kept %d inline bytes, want cleared", len(evt.Parts[0].Data))
			}
			if evt.Parts[0].ImagePath == "" {
				t.Error("event ImagePath is empty, want the session-owned file")
			}
			return
		case <-deadline.C:
			t.Fatal("timed out waiting for UserMessageCommittedEvent")
		}
	}
}
