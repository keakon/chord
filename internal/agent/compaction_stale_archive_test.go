package agent

import (
	"os"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestStaleReadyCompactionCleansExportedArchive(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	historyPath, _, _, err := a.exportCompactionHistory(
		[]message.Message{{Role: message.RoleUser, Content: "stale archive"}},
		5,
		nil,
		a.captureCompactionArchiveMeta(),
	)
	if err != nil {
		t.Fatalf("export stale history: %v", err)
	}

	a.startCompactionState(
		6,
		compactionTarget{sessionEpoch: a.sessionEpoch},
		compactionTriggerUsageDriven,
		continuationPlan{kind: compactionResumeIdle},
	)
	a.handleCompactionReady(Event{
		Type: EventCompactionReady,
		Payload: &compactionDraft{
			PlanID:         5,
			Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
			NewMessages:    []message.Message{{Role: message.RoleUser, Content: "[Context Summary]\nsuperseded"}},
			AbsHistoryPath: historyPath,
		},
	})

	if !a.IsCompactionRunning() {
		t.Fatal("stale ready event must not release the newer compaction's state")
	}
	for _, path := range []string{historyPath, compactionHistoryMetaPath(historyPath)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale compaction file %q still exists, err=%v", path, err)
		}
	}
}
