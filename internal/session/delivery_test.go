package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/filedelivery"
	"github.com/keakon/chord/internal/message"
)

func TestDeliveryExportImportPreservesMissingArtifact(t *testing.T) {
	file := message.DeliveredFile{ID: "delivery", Source: message.DeliverySource{SessionID: "source", TaskID: "task"}, Locator: message.FileLocator{Kind: message.FileLocatorArtifact, Artifact: &message.ArtifactFileLocator{RelPath: "artifacts/report.txt"}}, SHA256: "a", DisplayName: "report.txt"}
	msgs := []message.Message{{Role: message.RoleUser, Content: "summary", IsCompactionSummary: true, CompactionDeliveredFiles: []message.DeliveredFile{file}}, {Role: message.RoleAssistant, Content: "[report](report.txt)", FileLinkBaseDir: t.TempDir()}, {Role: message.RoleTool, ToolCallID: "call", DeliveredFiles: []message.DeliveredFile{file}}}
	exported, err := Export(msgs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.json")
	if err := ExportToFile(exported, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var imported ExportedSession
	if err := json.Unmarshal(data, &imported); err != nil {
		t.Fatal(err)
	}
	restored := imported.ToMessages()
	if !reflect.DeepEqual(restored[0].CompactionDeliveredFiles, msgs[0].CompactionDeliveredFiles) || !reflect.DeepEqual(restored[2].DeliveredFiles, msgs[2].DeliveredFiles) || restored[1].FileLinkBaseDir != msgs[1].FileLinkBaseDir {
		t.Fatal("delivery identity lost in export/import")
	}
	if _, err := filedelivery.Verify(context.Background(), restored[2].DeliveredFiles[0], t.TempDir()); err == nil {
		t.Fatal("missing imported resource accepted")
	}
	restored[0].CompactionDeliveredFiles[0].Locator.Artifact.RelPath = "artifacts/changed.txt"
	if exported.Messages[0].CompactionDeliveredFiles[0].Locator.Artifact.RelPath != file.Locator.Artifact.RelPath {
		t.Fatal("restored locator aliases export")
	}
}
