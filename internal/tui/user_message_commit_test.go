package tui

import (
	"bytes"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func TestApplyCommittedUserMessagePartsDropsInlineData(t *testing.T) {
	block := &Block{
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{Data: []byte("inline"), ImagePath: ""}},
		PDFNames:   []string{"old.pdf"},
	}
	parts := []message.ContentPart{
		{Type: message.ContentPartImage, MimeType: "image/png", ImagePath: "/session/images/a.png", DataBytes: 123},
		{Type: message.ContentPartPDF, MimeType: "application/pdf", ImagePath: "/session/images/b.pdf", FileName: "b.pdf", DataBytes: 456},
	}

	applyCommittedUserMessageParts(block, parts)

	if len(block.ImageParts) != 1 {
		t.Fatalf("ImageParts = %d, want 1", len(block.ImageParts))
	}
	if len(block.ImageParts[0].Data) != 0 {
		t.Errorf("ImageParts[0].Data kept %d bytes, want cleared", len(block.ImageParts[0].Data))
	}
	if block.ImageParts[0].ImagePath != "/session/images/a.png" {
		t.Errorf("ImageParts[0].ImagePath = %q, want the durable path", block.ImageParts[0].ImagePath)
	}
	if block.ImageCount != 1 {
		t.Errorf("ImageCount = %d, want 1", block.ImageCount)
	}
	if len(block.PDFNames) != 1 || block.PDFNames[0] != "b.pdf" {
		t.Errorf("PDFNames = %v, want [b.pdf]", block.PDFNames)
	}
}

func TestCommittedUserMessageMatchesReceiptAndOwner(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	parts := []message.ContentPart{{Type: message.ContentPartImage, Data: []byte("inline")}}
	var blocks []*Block
	for _, owner := range []string{"worker-1", "worker-1", "main"} {
		requestID := "request-1"
		if len(blocks) == 1 {
			requestID = "request-2"
		}
		block := &Block{ID: m.nextBlockID, Type: BlockUser, AgentID: owner, InputRequestID: requestID, MsgIndex: 0, Content: "same text", ImageCount: 1, ImageParts: imagePartsFromContentParts(parts)}
		m.nextBlockID++
		m.appendViewportBlock(block)
		blocks = append(blocks, block)
	}
	m.inflightDraft = &queuedDraft{ID: "request-1", AgentID: "worker-1", Parts: parts}
	canonical := []message.ContentPart{{Type: message.ContentPartImage, ImagePath: "/session/images/sample.png", DataBytes: 6}}
	m.handleTurnAgentEvent(agent.UserMessageCommittedEvent{AgentID: "worker-1", RequestID: "request-1", MessageIndex: 7, Parts: canonical})
	if len(blocks[0].ImageParts[0].Data) != 0 || blocks[0].ImageParts[0].ImagePath == "" || blocks[0].MsgIndex != 7 {
		t.Fatalf("matching card not updated: %#v", blocks[0])
	}
	for _, block := range blocks[1:] {
		if !bytes.Equal(block.ImageParts[0].Data, parts[0].Data) {
			t.Fatalf("unrelated card was changed: %#v", block)
		}
	}
	if len(m.inflightDraft.Parts[0].Data) != 0 || m.inflightDraft.Parts[0].ImagePath == "" {
		t.Fatalf("inflight payload retained: %#v", m.inflightDraft)
	}
}

func TestCommittedUserMessagePrefersNewSubmissionOverRestoredReceipt(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	for i := range 2 {
		m.appendViewportBlock(&Block{
			ID: i, Type: BlockUser, InputRequestID: "draft-1", MsgIndex: i,
			ImageCount: 1, ImageParts: []BlockImagePart{{Data: []byte("inline")}},
		})
	}
	// Local draft counters restart with the TUI; historical request IDs can
	// match the new submission. The optimistic card is the newest one.
	m.handleTurnAgentEvent(agent.UserMessageCommittedEvent{RequestID: "draft-1", MessageIndex: 1, Parts: []message.ContentPart{{Type: message.ContentPartImage, ImagePath: "/session/images/sample.png"}}})
	if len(m.viewport.blocks[0].ImageParts[0].Data) == 0 || len(m.viewport.blocks[1].ImageParts[0].Data) != 0 {
		t.Fatal("new submission updated a historical card with the same receipt")
	}
}

func TestCommittedUserMessageReleasesDraftWithoutViewportCard(t *testing.T) {
	for _, mode := range []string{"no viewport", "empty viewport"} {
		t.Run(mode, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 24)
			if mode == "no viewport" {
				m.viewport = nil
			}
			m.inflightDraft = &queuedDraft{ID: "request-1", AgentID: "worker-1", Parts: []message.ContentPart{{Type: message.ContentPartPDF, Data: []byte("inline")}}}
			m.handleTurnAgentEvent(agent.UserMessageCommittedEvent{AgentID: "worker-1", RequestID: "request-1", MessageIndex: 0})
			if len(m.inflightDraft.Parts) != 0 {
				t.Fatal("dropped attachment retained in draft without a card")
			}
			if m.viewport != nil && len(m.viewport.blocks) != 0 {
				t.Fatal("commit event created a new card")
			}
		})
	}
}

func TestCommittedUserMessageFallbackDoesNotUseFocusedTranscript(t *testing.T) {
	backend := &focusedMessagesAgent{focused: "worker-1", messagesByFocus: map[string][]message.Message{
		"worker-1": {{Role: message.RoleUser, Content: "other"}},
	}}
	m := NewModelWithSize(backend, 80, 24)
	main := &Block{ID: 1, Type: BlockUser, MsgIndex: 0, Content: "main", ImageCount: 1, ImageParts: []BlockImagePart{{Data: []byte("main")}}}
	worker := &Block{ID: 2, Type: BlockUser, AgentID: "worker-1", MsgIndex: 0, Content: "other", ImageCount: 1, ImageParts: []BlockImagePart{{Data: []byte("worker")}}}
	m.appendViewportBlock(main)
	m.appendViewportBlock(worker)
	m.handleTurnAgentEvent(agent.UserMessageCommittedEvent{MessageIndex: 0, Parts: []message.ContentPart{{Type: message.ContentPartImage, ImagePath: "/session/images/main.png"}}})
	if len(main.ImageParts[0].Data) != 0 || string(worker.ImageParts[0].Data) != "worker" {
		t.Fatal("index fallback did not respect message owner")
	}
}

func TestCommittedUserMessagePreservesPayloadOnNormalizationFailure(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	parts := []message.ContentPart{
		{Type: message.ContentPartImage, Data: []byte("image")},
		{Type: message.ContentPartPDF, Data: []byte("pdf")},
	}
	m.stageDraft(queuedDraft{ID: "request-1", Parts: parts})
	m.handleTurnAgentEvent(agent.UserMessageCommittedEvent{RequestID: "request-1", MessageIndex: 0, Parts: parts})
	block := m.viewport.blocks[0]
	if !bytes.Equal(block.ImageParts[0].Data, parts[0].Data) || !bytes.Equal(m.inflightDraft.Parts[1].Data, parts[1].Data) {
		t.Fatal("commit cleared inline data without a durable replacement")
	}
	m.handleTurnAgentEvent(agent.UserMessageCommittedEvent{RequestID: "request-1", MessageIndex: 0})
	if block.ImageCount != 0 || len(block.PDFNames) != 0 || len(m.inflightDraft.Parts) != 0 {
		t.Fatal("filtered payload not released")
	}
}

func TestQueuedSubAgentCardAdoptsCommittedParts(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	queuedAt := time.Now()
	m.queuedDrafts = []queuedDraft{{
		ID: "draft-1", AgentID: "worker-1", Content: "inspect", LoopAnchor: true, QueuedAt: queuedAt, FileRefs: []string{"sample.go"},
		Parts: []message.ContentPart{{Type: message.ContentPartImage, Data: []byte("image")}, {Type: message.ContentPartPDF, Data: []byte("pdf")}},
	}}
	parts := []message.ContentPart{
		{Type: message.ContentPartText, Text: "inspect"},
		{Type: message.ContentPartImage, ImagePath: "/session/images/image.png", DataBytes: 5},
		{Type: message.ContentPartPDF, ImagePath: "/session/images/sample.pdf", FileName: "sample.pdf", DataBytes: 3},
	}
	m.handleTurnAgentEvent(agent.PendingDraftConsumedEvent{AgentID: "worker-1", DraftID: "draft-1", Parts: parts})
	if len(m.queuedDrafts) != 0 || len(m.viewport.blocks) != 1 {
		t.Fatalf("queue=%d cards=%d", len(m.queuedDrafts), len(m.viewport.blocks))
	}
	block := m.viewport.blocks[0]
	if len(block.ImageParts[0].Data) != 0 || block.ImageParts[0].ImagePath == "" || len(block.PDFNames) != 1 {
		t.Fatal("queued card kept inline payload")
	}
	if !block.LoopAnchor || !block.StartedAt.Equal(queuedAt) || len(block.FileRefs) != 1 {
		t.Fatal("queued UI metadata was lost")
	}
	for _, part := range m.inflightDraft.Parts {
		if part.IsBinary() && (len(part.Data) != 0 || part.ImagePath == "") {
			t.Fatalf("queued inflight payload retained: %#v", part)
		}
	}
}

func TestCommittedReceiptSurvivesFocusRebuildAndRehydration(t *testing.T) {
	backend := &targetedConversationAgent{
		messagesByFocus: map[string][]message.Message{
			"worker-2": {{Role: message.RoleUser, InputRequestID: "request-1", Parts: []message.ContentPart{{Type: message.ContentPartImage, Data: []byte("inline")}}}},
		},
	}
	m := NewModelWithSize(backend, 80, 24)
	m.inflightDraft = &queuedDraft{ID: "request-1", AgentID: "worker-1", Parts: []message.ContentPart{{Type: message.ContentPartImage, Data: []byte("inline")}}}
	m.setFocusedAgent("worker-1")
	m.handleSubAgentEvent(agent.AgentStartedEvent{AgentID: "worker-2", PreviousAgentID: "worker-1"})
	m.handleTurnAgentEvent(agent.UserMessageCommittedEvent{AgentID: "worker-2", RequestID: "request-1", MessageIndex: 0, Parts: []message.ContentPart{{Type: message.ContentPartImage, ImagePath: "/session/images/sample.png", DataBytes: 6}}})
	if len(m.viewport.blocks) != 1 || len(m.viewport.blocks[0].ImageParts[0].Data) != 0 || m.viewport.blocks[0].InputRequestID != "request-1" {
		t.Fatal("rebuilt card did not preserve receipt identity")
	}
	if m.inflightDraft.AgentID != "worker-2" || len(m.inflightDraft.Parts[0].Data) != 0 {
		t.Fatal("rehydrated inflight draft did not adopt committed parts")
	}
}

func TestSendMultipartDraftCapturesTargetAndReceipt(t *testing.T) {
	backend := &targetedConversationAgent{}
	backend.subAgents = []agent.SubAgentInfo{{InstanceID: "worker-1", TaskID: "task-1"}}
	m := NewModelWithSize(backend, 80, 24)
	m.setFocusedAgent("worker-1")
	m.sendDraft(queuedDraft{Parts: []message.ContentPart{{Type: message.ContentPartImage, Data: []byte("inline")}}})
	if len(backend.targetedCalls) != 1 || backend.targetedCalls[0] != "send-parts:worker-1/task-1:draft-1" {
		t.Fatalf("targeted calls = %v", backend.targetedCalls)
	}
	if m.inflightDraft.ID != "draft-1" || m.viewport.blocks[0].InputRequestID != "draft-1" {
		t.Fatal("draft and card do not share the submission receipt")
	}
}
