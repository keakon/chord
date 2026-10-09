package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNativeRequestJournalCrashWindows(t *testing.T) {
	for _, state := range []string{"authorization_only", "unknown", "completed", "corrupt"} {
		t.Run(state, func(t *testing.T) {
			j := NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "main", TurnID: 1}
			id, err := j.Begin(map[string]string{"target": "sample/test-model"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp := &message.Response{Content: "Search completed."}
			switch state {
			case "unknown":
				err = j.Finish(id, message.NativeRequestUnknown, resp, errors.New("connection closed"))
			case "completed":
				err = j.Finish(id, message.NativeRequestCompleted, resp, nil)
			case "corrupt":
				err = os.WriteFile(filepath.Join(j.directory(), id+".result.json"), []byte("{"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := j.Begin(nil, nil); err == nil {
				t.Fatal("unacknowledged request released replay barrier")
			}
			_, err = j.Begin(nil, []string{id})
			if (err == nil) != (state == "completed") {
				t.Fatalf("continuation err=%v", err)
			}
			err = j.Acknowledge(&message.NativeToolHistory{RequestIDs: []string{id}})
			if (err == nil) != (state == "completed") {
				t.Fatalf("ack err=%v", err)
			}
			other := j
			other.AgentID = "worker"
			if _, err := other.Begin(nil, nil); err != nil {
				t.Fatalf("agent isolation: %v", err)
			}
		})
	}
}
func TestNativeRequestJournalAcknowledgedAndPrivate(t *testing.T) {
	j := NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "main"}
	id, err := j.Begin(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Finish(id, message.NativeRequestCompleted, &message.Response{}, nil); err != nil {
		t.Fatal(err)
	}
	if err = j.Acknowledge(&message.NativeToolHistory{RequestIDs: []string{id}}); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Begin(nil, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(j.directory(), id+".request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("public authorization: %v", info.Mode())
	}
	if err = j.Acknowledge(&message.NativeToolHistory{RequestIDs: []string{"../escape"}}); err == nil {
		t.Fatal("unsafe ID accepted")
	}
}

func TestNativeUnexecutedReceiptMustBeDurable(t *testing.T) {
	for _, outcome := range []message.NativeRequestOutcome{message.NativeRequestNotSent, message.NativeRequestRejected} {
		t.Run(string(outcome), func(t *testing.T) {
			j := NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "main"}
			id, err := j.Begin(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Mkdir(filepath.Join(j.directory(), id+".result.json"), 0700); err != nil {
				t.Fatal(err)
			}
			if err = j.Finish(id, outcome, nil, errors.New("request refused")); err == nil {
				t.Fatal("receipt write should fail")
			}
			if _, err = j.Check(); err == nil {
				t.Fatal("failed persistence released barrier")
			}
		})
	}
}

func TestNativeRequestOutcomeValidation(t *testing.T) {
	j := NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "main"}
	id, err := j.Begin(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []message.NativeRequestOutcome{message.NativeRequestUnknown, message.NativeRequestRejected, message.NativeRequestNotSent, "invalid"} {
		if err = j.Finish(id, outcome, nil, nil); err == nil {
			t.Fatalf("accepted invalid outcome %s", outcome)
		}
	}
	if err = j.Finish(id, message.NativeRequestCompleted, nil, nil); err == nil {
		t.Fatal("accepted missing completed response")
	}
	if err = j.Finish(id, message.NativeRequestCompleted, &message.Response{}, errors.New("failed")); err == nil {
		t.Fatal("accepted failed completion")
	}
	if _, err = j.Check(); err == nil {
		t.Fatal("invalid result released barrier")
	}
}
