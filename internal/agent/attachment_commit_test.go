package agent

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// A committed user attachment must leave no inline payload in the transcript
// AND no inline payload in memory. This is the end-to-end counterpart to the
// recovery unit tests: it runs the real commit path (normalize -> ctxMgr ->
// async persist) and then reads what actually landed in main.jsonl.
//
// The JSONL half alone would NOT catch a regression: the legacy
// persistBinaryParts already wrote a file and cleared Data on its own copy for
// a clipboard-pasted part, so the record looked clean while the live history
// kept the bytes. The resident byte count below is the assertion that actually
// distinguishes the two, which is why it must not be dropped as redundant.
func TestCommittedUserAttachmentPersistsAsPathNotInlineData(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	payload := []byte("\x89PNG\r\n\x1a\nattachment-payload")
	a.recordCommittedUserMessage(message.Message{
		Role:    message.RoleUser,
		Content: "what is in this screenshot?",
		Parts: []message.ContentPart{{
			Type:     message.ContentPartImage,
			MimeType: "image/png",
			FileName: "shot.png",
			Data:     payload,
		}},
	})
	a.flushPersist()

	record, err := os.ReadFile(filepath.Join(a.SessionDir(), "main.jsonl"))
	if err != nil {
		t.Fatalf("read main.jsonl: %v", err)
	}
	// ContentPart.Data is `json:"data,omitempty"`, so an inline payload shows up
	// as a "data" key. Its absence is what proves the bytes never became durable.
	if bytes.Contains(record, []byte(`"data"`)) {
		t.Fatalf("main.jsonl still carries inline attachment data:\n%s", record)
	}
	if !bytes.Contains(record, []byte(`"image_path"`)) {
		t.Fatalf("main.jsonl has no image_path for the attachment:\n%s", record)
	}
	if encoded := base64.StdEncoding.EncodeToString(payload); bytes.Contains(record, []byte(encoded)) {
		t.Fatal("main.jsonl contains the base64 payload")
	}

	// The referenced file exists inside the session and holds the exact bytes.
	path := imagePathFromRecord(t, record)
	if !strings.HasPrefix(path, filepath.Join(a.SessionDir(), "images")+string(filepath.Separator)) {
		t.Fatalf("attachment path %q is outside the session images dir", path)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored attachment: %v", err)
	}
	if !bytes.Equal(stored, payload) {
		t.Fatalf("stored attachment = %q, want %q", stored, payload)
	}

	// The [memory] contract: the history no longer holds image payload bytes.
	if got := a.GetResidentMemoryBreakdown().Image; got != 0 {
		t.Fatalf("resident image_bytes = %d after commit, want 0 (only metadata remains)", got)
	}
	// ...while the context accounting still counts the attachment, because the
	// model receives those bytes on the next request. If this were also zero the
	// two accountings would have collapsed and the image would never be sent.
	if got, want := a.ctxMgr.BytesBreakdown().Image, len(payload); got != want {
		t.Fatalf("context image_bytes = %d after commit, want %d", got, want)
	}
}

// imagePathFromRecord extracts the image_path value from the persisted JSONL
// line without depending on the full message schema.
func imagePathFromRecord(t *testing.T, record []byte) string {
	t.Helper()
	const key = `"image_path":"`
	_, rest, ok := bytes.Cut(record, []byte(key))
	if !ok {
		t.Fatalf("no image_path in record:\n%s", record)
		return ""
	}
	path, _, ok := bytes.Cut(rest, []byte(`"`))
	if !ok {
		t.Fatalf("unterminated image_path in record:\n%s", record)
		return ""
	}
	return string(path)
}
