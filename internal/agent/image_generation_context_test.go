package agent

import (
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestGeneratedImagePreviewProjectionPreservesHistory(t *testing.T) {
	images := []message.ContentPart{
		{Type: message.ContentPartText, Text: "All originals saved"},
		{Type: message.ContentPartImage, ArtifactID: "sha256-first", ImagePath: "first.png"},
		{Type: message.ContentPartImage, ArtifactID: "sha256-second", ImagePath: "second.png"},
		{Type: message.ContentPartImage, ImagePath: "reference.png"},
		{Type: message.ContentPartPDF, ImagePath: "document.pdf"},
		{Type: message.ContentPartImage, ArtifactID: "sha256-third", ImagePath: "third.png"},
		{Type: message.ContentPartText, Text: "File references"},
	}
	history := []message.Message{{Role: message.RoleUser, Parts: images}, {Role: message.RoleTool, Parts: images}, {Role: message.RoleTool, Parts: images}}
	before := append([]message.ContentPart(nil), images...)
	request := projectGeneratedImagePreviews(history)
	want := []message.ContentPart{images[0], images[1], images[3], images[4], images[6]}
	for _, i := range []int{1, 2} {
		if !reflect.DeepEqual(request[i].Parts, want) {
			t.Fatalf("tool %d preview selection=%+v", i, request[i].Parts)
		}
	}
	if !reflect.DeepEqual(request[0].Parts, images) {
		t.Fatal("user attachments were filtered")
	}
	request[1].Parts[1].ImagePath = "request-only.png"
	if !reflect.DeepEqual(history[1].Parts, before) || !reflect.DeepEqual(history[2].Parts, before) {
		t.Fatal("request projection changed durable originals")
	}
}

func TestGeneratedImagePreviewProjectionUsedByRequestEntrypoints(t *testing.T) {
	history := []message.Message{{Role: message.RoleTool, Content: "Saved originals", Parts: []message.ContentPart{
		{Type: message.ContentPartImage, ArtifactID: "sha256-first", ImagePath: "first.png"},
		{Type: message.ContentPartImage, ArtifactID: "sha256-second", ImagePath: "second.png"},
	}}}
	main := (&MainAgent{}).prepareMessagesForLLMWithOptions(history, false)
	worker := (&SubAgent{}).prepareContextForLLM(history)
	retry, _ := filterUnsupportedBinaryPartsForModel(history, nil)
	for _, request := range [][]message.Message{main, worker, retry} {
		if len(request[0].Parts) != 1 || request[0].Parts[0].ImagePath != "first.png" {
			t.Fatal("request entrypoint sent the full gallery")
		}
	}
	if len(history[0].Parts) != 2 {
		t.Fatal("history lost an original")
	}
	unchanged := projectGeneratedImagePreviews(main)
	if &unchanged[0] != &main[0] {
		t.Fatal("no-op projection copied messages")
	}
}
