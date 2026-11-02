package ctxmgr

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestMessageBytesBreakdownAttributesEveryBucket(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "hello"},
		{Role: message.RoleUser, Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "abc"},
			{Type: message.ContentPartImage, MimeType: "image/png", DataBytes: 1000},
			{Type: message.ContentPartPDF, MimeType: "application/pdf", DataBytes: 2000},
		}},
		{Role: message.RoleAssistant, Content: "answer", ToolCalls: []message.ToolCall{
			{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}, ThinkingBlocks: []message.ThinkingBlock{{Thinking: "why"}},
			ResponsesOutput:  []message.ResponsesOutputItem{{ID: "rs-1", EncryptedContent: "enc"}},
			GeminiParts:      []message.GeminiReplayPart{{ThoughtSignature: "sig"}},
			ReasoningContent: "reason"},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "result"},
	}

	b := MessageBytesBreakdown(msgs)
	if b.Image != 1000 {
		t.Fatalf("Image = %d, want 1000", b.Image)
	}
	if b.PDF != 2000 {
		t.Fatalf("PDF = %d, want 2000", b.PDF)
	}
	if b.Text == 0 || b.ToolCalls == 0 || b.Thinking == 0 || b.Responses == 0 || b.Gemini == 0 {
		t.Fatalf("bucket left empty: %+v", b)
	}
	if got, want := b.Total(), messageContextBytes(msgs); got != want {
		t.Fatalf("Total() = %d, messageContextBytes = %d", got, want)
	}
	if got, want := b.Total(), EstimateMessagesBytes(msgs); got != want {
		t.Fatalf("Total() = %d, EstimateMessagesBytes = %d", got, want)
	}
}

func TestManagerBytesBreakdownMatchesEstimate(t *testing.T) {
	m := NewManager(200000, 0.8)
	m.Append(message.Message{Role: message.RoleUser, Content: "hello"})
	m.Append(message.Message{Role: message.RoleUser, Parts: []message.ContentPart{
		{Type: message.ContentPartImage, MimeType: "image/png", DataBytes: 2048},
	}})

	b := m.BytesBreakdown()
	if b.Image != 2048 {
		t.Fatalf("Image = %d, want 2048", b.Image)
	}
	if got, want := b.Total(), EstimateMessagesBytes(m.Snapshot()); got != want {
		t.Fatalf("Total() = %d, EstimateMessagesBytes = %d", got, want)
	}
}

// The two accountings must differ in exactly one place: an attachment that has
// been moved to the session directory still occupies context but no longer
// occupies the heap. Reporting the on-disk size as resident memory is what makes
// the [memory] gauge lie.
func TestMessageResidentBytesIgnoresExternalizedAttachments(t *testing.T) {
	inline := []message.Message{{Role: message.RoleUser, Parts: []message.ContentPart{
		{Type: message.ContentPartImage, MimeType: "image/png", Data: make([]byte, 4096)},
	}}}
	externalized := []message.Message{{Role: message.RoleUser, Parts: []message.ContentPart{
		{Type: message.ContentPartImage, MimeType: "image/png", ImagePath: "/session/images/1-0.png", DataBytes: 4096},
	}}}

	if got := MessageBytesBreakdown(inline).Image; got != 4096 {
		t.Fatalf("context Image for inline data = %d, want 4096", got)
	}
	if got := MessageResidentBytesBreakdown(inline).Image; got != 4096 {
		t.Fatalf("resident Image for inline data = %d, want 4096", got)
	}
	// Context keeps counting it: the model still receives the bytes.
	if got := MessageBytesBreakdown(externalized).Image; got != 4096 {
		t.Fatalf("context Image for a path-only part = %d, want 4096", got)
	}
	if got := MessageResidentBytesBreakdown(externalized).Image; got != 0 {
		t.Fatalf("resident Image for a path-only part = %d, want 0", got)
	}

	// Every non-attachment bucket must be identical in both modes.
	text := []message.Message{
		{Role: message.RoleUser, Content: "hello"},
		{Role: message.RoleAssistant, Content: "answer", ToolCalls: []message.ToolCall{
			{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}, ThinkingBlocks: []message.ThinkingBlock{{Thinking: "why"}},
			ResponsesOutput: []message.ResponsesOutputItem{{ID: "rs-1", EncryptedContent: "enc"}},
			GeminiParts:     []message.GeminiReplayPart{{ThoughtSignature: "sig"}}},
	}
	contextBytes, residentBytes := MessageBytesBreakdown(text), MessageResidentBytesBreakdown(text)
	if contextBytes != residentBytes {
		t.Fatalf("accountings differ without attachments: context=%+v resident=%+v", contextBytes, residentBytes)
	}
}

func TestPDFBytesRemainSeparateAfterExternalization(t *testing.T) {
	msgs := []message.Message{{Role: message.RoleUser, Parts: []message.ContentPart{
		{Type: message.ContentPartText, Text: "inspect"},
		{Type: message.ContentPartImage, Data: make([]byte, 1024)},
		{Type: message.ContentPartPDF, Data: make([]byte, 4096)},
	}}}
	inline := MessageBytesBreakdown(msgs)
	if inline.Text != len("inspect") || inline.Image != 1024 || inline.PDF != 4096 {
		t.Fatalf("inline breakdown = %+v", inline)
	}
	if resident := MessageResidentBytesBreakdown(msgs); resident != inline {
		t.Fatalf("inline resident = %+v, want %+v", resident, inline)
	}
	for i := range msgs[0].Parts {
		part := &msgs[0].Parts[i]
		if part.IsBinary() {
			part.DataBytes = int64(len(part.Data))
			part.Data = nil
			part.ImagePath = "/session/images/attachment"
		}
	}
	wire, resident := MessageBytesBreakdown(msgs), MessageResidentBytesBreakdown(msgs)
	if wire != inline || wire.Total() != EstimateMessagesBytes(msgs) {
		t.Fatalf("externalization changed context accounting: inline=%+v wire=%+v", inline, wire)
	}
	if resident.Text != inline.Text || resident.Image != 0 || resident.PDF != 0 {
		t.Fatalf("externalized resident = %+v", resident)
	}
}
