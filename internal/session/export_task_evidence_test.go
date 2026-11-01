package session

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestExportTaskEvidenceRoundTripAndIsolation(t *testing.T) {
	original := &message.TaskEvidence{
		TaskID:                    "task-1",
		ChangedPaths:              []string{"observed.go"},
		FileAttributionIncomplete: true,
	}
	exported, err := Export([]message.Message{{
		Role: message.RoleUser, IsCompactionSummary: true, CompactionTaskEvidence: original,
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExportedSession
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	restored := decoded.ToMessages()
	if !reflect.DeepEqual(restored[0].CompactionTaskEvidence, original) {
		t.Fatalf("task evidence lost on export/import: %#v", restored[0].CompactionTaskEvidence)
	}
	restored[0].CompactionTaskEvidence.ChangedPaths[0] = "mutated.go"
	if original.ChangedPaths[0] != "observed.go" {
		t.Fatal("shared task evidence slice on export/import")
	}

	plain, err := Export([]message.Message{{Role: message.RoleUser, Content: "sample"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Messages[0].CompactionTaskEvidence != nil {
		t.Fatalf("absent task evidence invented: %#v", plain.Messages[0].CompactionTaskEvidence)
	}
	if plain.ToMessages()[0].CompactionTaskEvidence != nil {
		t.Fatal("absent task evidence invented on import")
	}
}
