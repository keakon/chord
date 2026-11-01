package message

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTaskEvidenceRoundTripAndClone(t *testing.T) {
	t.Parallel()
	for _, evidence := range []*TaskEvidence{
		nil,
		{TaskID: "task-1", ChangedPaths: []string{"sample.go"}, FileAttributionIncomplete: true},
		{TaskID: "task-1", FileAttributionIncomplete: true},
	} {
		msg := Message{Role: RoleUser, IsCompactionSummary: true, CompactionTaskEvidence: evidence}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		var restored Message
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(restored.CompactionTaskEvidence, evidence) {
			t.Fatalf("round trip = %#v, want %#v", restored.CompactionTaskEvidence, evidence)
		}
		cloned := msg.Clone()
		if !reflect.DeepEqual(cloned.CompactionTaskEvidence, evidence) {
			t.Fatalf("clone = %#v, want %#v", cloned.CompactionTaskEvidence, evidence)
		}
		if evidence != nil {
			cloned.CompactionTaskEvidence.TaskID = "task-2"
			if evidence.TaskID != "task-1" {
				t.Fatal("clone shares the evidence pointer")
			}
			if len(evidence.ChangedPaths) > 0 {
				cloned.CompactionTaskEvidence.ChangedPaths[0] = "other.go"
				if evidence.ChangedPaths[0] != "sample.go" {
					t.Fatal("clone shares the paths slice")
				}
			}
		}
	}
}
