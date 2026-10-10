package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestSessionForkOwnsGeneratedImagesInMemoryAndHistory(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "local"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			src := a.SessionDir()
			fixtureDir, _, reference := imageAuthorizationFixture(t)
			img, err := tools.ReadGeneratedOriginal(t.Context(), fixtureDir, reference)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := tools.SaveImageArtifact(t.Context(), src, img)
			if err != nil {
				t.Fatal(err)
			}
			part := message.ContentPart{Type: message.ContentPartImage, MimeType: img.MIME, ImagePath: filepath.Join(src, ref.RelPath), ArtifactID: ref.ID}
			summary, err := json.Marshal(tools.ImageGenerationSummary{State: imagegen.StateSaved, Images: []tools.GeneratedImage{{ArtifactRef: ref, Reference: reference, Width: img.Width, Height: img.Height}}})
			if err != nil {
				t.Fatal(err)
			}
			imageMsg := message.Message{Role: message.RoleTool, Content: string(summary), ToolPayload: string(summary), Parts: []message.ContentPart{part}}
			if native {
				imageMsg = message.Message{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{Calls: []message.HostedCall{{ID: "image-1", Kind: message.HostedCallKindImageGeneration, Result: summary, Parts: []message.ContentPart{part}}}}}
			}
			msgs := []message.Message{{Role: message.RoleUser, Content: "Generate a tree"}, imageMsg, {Role: message.RoleUser, Content: "Edit the tree"}, {Role: message.RoleAssistant, Content: "Ready"}}
			a.ctxMgr.RestoreMessages(msgs)
			a.handleForkSessionCommand(2)
			dst := a.SessionDir()
			if dst == src {
				t.Fatal("fork did not switch sessions")
			}
			if err := os.RemoveAll(src); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dst, "main.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var persisted message.Message
			lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
			if err := json.Unmarshal(lines[1], &persisted); err != nil {
				t.Fatal(err)
			}
			for _, msg := range []message.Message{a.ctxMgr.Snapshot()[1], persisted} {
				payload, parts := msg.ToolPayload, msg.Parts
				if native {
					payload, parts = string(msg.NativeTools.Calls[0].Result), msg.NativeTools.Calls[0].Parts
					if imageMsg.NativeTools.Calls[0].Parts[0].ImagePath != part.ImagePath {
						t.Fatal("fork mutated source native receipt")
					}
				}
				if err := tools.VisitGeneratedOriginals(t.Context(), payload, parts, func(_ tools.GeneratedImage, original imagegen.Image) error {
					if !bytes.Equal(original.Data, img.Data) {
						t.Fatal("fork normalized original bytes")
					}
					return nil
				}); err != nil {
					t.Fatalf("fork message cannot deliver original: %v", err)
				}
			}
			// Missing originals must abort seeding before changing the active session.
			if err := os.Remove(filepath.Join(dst, ref.RelPath)); err != nil {
				t.Fatal(err)
			}
			root, err := a.projectSessionsDir()
			if err != nil {
				t.Fatal(err)
			}
			before, err := recovery.ListSessions(root, "")
			if err != nil {
				t.Fatal(err)
			}
			beforeEntries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			knownDirs := make(map[string]bool)
			for _, entry := range beforeEntries {
				if entry.IsDir() {
					knownDirs[entry.Name()] = true
				}
			}
			a.ctxMgr.RestoreMessages([]message.Message{{Role: message.RoleUser, Content: "A sample request"}, a.ctxMgr.Snapshot()[1], {Role: message.RoleUser, Content: "Edit again"}, {Role: message.RoleAssistant, Content: "Ready"}})
			a.handleForkSessionCommand(2)
			if a.SessionDir() != dst {
				t.Fatal("failed image fork replaced the active session")
			}
			after, err := recovery.ListSessions(root, "")
			if err != nil || len(after) != len(before) {
				t.Fatalf("failed image fork published a partial session: %d -> %d, %v", len(before), len(after), err)
			}
			afterEntries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range afterEntries {
				if entry.IsDir() && !knownDirs[entry.Name()] {
					t.Fatalf("failed fork retained an unprepared session directory: %s", entry.Name())
				}
			}

		})
	}
}
