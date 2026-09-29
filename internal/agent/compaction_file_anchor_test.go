package agent

import (
	"slices"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestCompactionFileAnchorIgnoresBinaryBytesAndPointerMetadata(t *testing.T) {
	base := message.Message{Role: message.RoleUser, Content: "look", Parts: []message.ContentPart{
		{Type: message.ContentPartText, Text: "look"},
		{Type: message.ContentPartImage, MimeType: "image/png", ImagePath: "/tmp/a.png", Data: []byte("0123")},
	}}
	restored := base
	restored.Parts = slices.Clone(base.Parts)
	restored.Parts[1].Data, restored.Parts[1].DataBytes = nil, 4
	restored.Usage = &message.TokenUsage{InputTokens: 1}
	restored.Provenance = &message.MessageProvenance{}
	if compactionFileAnchor(base) != compactionFileAnchor(restored) {
		t.Fatal("anchor depends on in-memory image bytes or pointer metadata")
	}
	edited := restored
	edited.Parts = slices.Clone(restored.Parts)
	edited.Parts[0].Text = "look again"
	if compactionFileAnchor(edited) == compactionFileAnchor(restored) {
		t.Fatal("anchor ignored a text change")
	}
	resized := restored
	resized.Parts = slices.Clone(restored.Parts)
	resized.Parts[1].DataBytes = 5
	if compactionFileAnchor(resized) == compactionFileAnchor(restored) {
		t.Fatal("anchor ignored a different image payload")
	}
}
