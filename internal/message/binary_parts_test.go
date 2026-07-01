package message

import "testing"

func TestFilterUnsupportedBinaryPartsNoOpWhenSupported(t *testing.T) {
	msgs := []Message{{
		Role:  RoleUser,
		Parts: []ContentPart{{Type: ContentPartText, Text: "hi"}, {Type: ContentPartImage, Data: []byte("png")}},
	}}
	got, counts := FilterUnsupportedBinaryParts(msgs, true, true)
	if counts.Any() {
		t.Fatalf("counts = %+v, want none dropped", counts)
	}
	if &got[0] != &msgs[0] {
		t.Fatal("expected original slice returned unchanged when both modalities supported")
	}
}

func TestFilterUnsupportedBinaryPartsNoOpWhenNothingDropped(t *testing.T) {
	msgs := []Message{{
		Role:  RoleUser,
		Parts: []ContentPart{{Type: ContentPartText, Text: "hi"}},
	}}
	got, counts := FilterUnsupportedBinaryParts(msgs, false, false)
	if counts.Any() {
		t.Fatalf("counts = %+v, want none dropped", counts)
	}
	if &got[0] != &msgs[0] {
		t.Fatal("expected original slice returned unchanged when nothing dropped")
	}
}

func TestFilterUnsupportedBinaryPartsDropsImageKeepingText(t *testing.T) {
	msgs := []Message{{
		Role:  RoleUser,
		Parts: []ContentPart{{Type: ContentPartText, Text: "what is this?"}, {Type: ContentPartImage, Data: []byte("png")}},
	}}
	got, counts := FilterUnsupportedBinaryParts(msgs, false, true)
	if counts.Images != 1 || counts.PDFs != 0 {
		t.Fatalf("counts = %+v, want 1 image dropped", counts)
	}
	if len(got) != 1 || len(got[0].Parts) != 1 || got[0].Parts[0].Type != ContentPartText {
		t.Fatalf("got = %#v, want text-only message", got)
	}
}

// TestFilterUnsupportedBinaryPartsDropsImageOnlyMessage is the regression guard
// for the divergence this helper unifies: an image-only user message (no text
// content) must be dropped entirely so wire encoders never receive an empty
// message, rather than left as a zero-parts message.
func TestFilterUnsupportedBinaryPartsDropsImageOnlyMessage(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Parts: []ContentPart{{Type: ContentPartImage, Data: []byte("png")}}},
		{Role: RoleUser, Content: "follow-up text"},
	}
	got, counts := FilterUnsupportedBinaryParts(msgs, false, false)
	if counts.Images != 1 {
		t.Fatalf("counts = %+v, want 1 image dropped", counts)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1 (image-only message dropped)", len(got))
	}
	if got[0].Content != "follow-up text" {
		t.Fatalf("surviving message = %#v, want follow-up text message", got[0])
	}
}

func TestFilterUnsupportedBinaryPartsKeepsImageOnlyMessageWithContent(t *testing.T) {
	msgs := []Message{{
		Role:    RoleTool,
		Content: "tool output",
		Parts:   []ContentPart{{Type: ContentPartImage, Data: []byte("png")}},
	}}
	got, counts := FilterUnsupportedBinaryParts(msgs, false, false)
	if counts.Images != 1 {
		t.Fatalf("counts = %+v, want 1 image dropped", counts)
	}
	if len(got) != 1 || len(got[0].Parts) != 0 {
		t.Fatalf("got = %#v, want message kept with parts removed", got)
	}
}

func TestFilterUnsupportedBinaryPartsDropsPDF(t *testing.T) {
	msgs := []Message{{
		Role:  RoleUser,
		Parts: []ContentPart{{Type: ContentPartText, Text: "read this"}, {Type: ContentPartPDF, Data: []byte("%PDF")}},
	}}
	got, counts := FilterUnsupportedBinaryParts(msgs, true, false)
	if counts.PDFs != 1 || counts.Images != 0 {
		t.Fatalf("counts = %+v, want 1 pdf dropped", counts)
	}
	if len(got) != 1 || len(got[0].Parts) != 1 || got[0].Parts[0].Type != ContentPartText {
		t.Fatalf("got = %#v, want text-only message", got)
	}
}
