package recovery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestSeedForkTranscriptPublishesCompleteHistoryAndAttachments(t *testing.T) {
	dir := t.TempDir()
	input := []message.Message{{Role: message.RoleUser, Content: "A sample request"}, {Role: message.RoleUser, Parts: []message.ContentPart{{Type: message.ContentPartPDF, MimeType: "application/pdf", Data: []byte("sample binary payload")}}}}
	seeded, err := SeedForkTranscript(dir, len(input), func(i int) (message.Message, error) {
		if _, err := os.Stat(filepath.Join(dir, "main.jsonl")); !os.IsNotExist(err) {
			t.Fatal("transcript was visible during preparation")
		}
		return input[i], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	part := seeded[1].Parts[0]
	if len(part.Data) != 0 || part.ImagePath == "" || part.DataBytes != int64(len(input[1].Parts[0].Data)) {
		t.Fatalf("memory history is not the persisted history: %+v", part)
	}
	data, err := os.ReadFile(part.ImagePath)
	if err != nil || !bytes.Equal(data, input[1].Parts[0].Data) {
		t.Fatalf("attachment changed: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "main.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != len(input) {
		t.Fatalf("incomplete transcript: %d", len(lines))
	}
	var persisted message.Message
	if err := json.Unmarshal(lines[1], &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Parts[0].ImagePath != part.ImagePath || len(input[1].Parts[0].Data) == 0 {
		t.Fatal("seed mutated source or diverged from disk")
	}
	if _, err := SeedForkTranscript(dir, 0, nil); err == nil {
		t.Fatal("existing transcript overwritten")
	}
}

func TestSeedForkTranscriptFailureIsNotDiscoverable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sample-session")
	_, err := SeedForkTranscript(dir, 2, func(i int) (message.Message, error) {
		if i == 1 {
			return message.Message{}, fmt.Errorf("attachment unavailable")
		}
		return message.Message{Role: message.RoleUser, Content: "A sample request"}, nil
	})
	if err == nil {
		t.Fatal("failed preparation reported success")
	}
	for _, name := range []string{"main.jsonl", "main.jsonl.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("failed fork retained %s: %v", name, err)
		}
	}
	listed, err := ListSessions(root, "")
	if err != nil || len(listed) != 0 {
		t.Fatalf("partial fork discoverable: %v, %v", listed, err)
	}
}
