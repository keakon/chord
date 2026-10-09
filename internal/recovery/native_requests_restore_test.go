package recovery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNativeJournalRestorePreservesUnknownAndReconcilesCanonical(t *testing.T) {
	for _, state := range []string{"authorization_only", "unknown", "completed", "canonical", "corrupt"} {
		t.Run(state, func(t *testing.T) {
			j := NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "task-1"}
			id, err := j.Begin(map[string]any{"target": "sample/test-model", "protocol": "responses", "api_url": "https://example.invalid/v1/responses", "authorization": map[string]any{"max_uses": 1}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			receipt := &message.NativeToolHistory{RequestIDs: []string{id}, Calls: []message.HostedCall{{ID: "search-1", Name: "web_search", Result: json.RawMessage(`[]`)}}}
			resp := &message.Response{Content: "Complete", NativeTools: receipt, StopReason: "stop"}
			var history []message.Message
			switch state {
			case "unknown":
				err = j.Finish(id, message.NativeRequestUnknown, resp, errors.New("connection closed"))
			case "completed", "canonical":
				err = j.Finish(id, message.NativeRequestCompleted, resp, nil)
				if state == "canonical" {
					history = []message.Message{{Role: message.RoleAssistant, NativeTools: receipt}}
				}
			case "corrupt":
				err = os.WriteFile(filepath.Join(j.directory(), id+".result.json"), []byte("{"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := j.Check(); err == nil {
				t.Fatal("unconfirmed request was allowed")
			}
			var writes []message.Message
			persist := func(msg message.Message) error { writes = append(writes, msg); return nil }
			history, err = j.Restore(history, persist)
			if err != nil {
				t.Fatal(err)
			}
			_, checkErr := j.Check()
			if state == "canonical" {
				if checkErr != nil || len(writes) != 0 {
					t.Fatalf("canonical reconciliation writes=%d err=%v", len(writes), checkErr)
				}
			} else {
				if checkErr == nil || len(writes) != 1 || len(history) != 1 || !history[0].NativeTools.OutcomeUnknown || history[0].NativeTools.Target != "sample/test-model" {
					t.Fatalf("history=%+v writes=%d err=%v", history, len(writes), checkErr)
				}
				if (state == "completed" || state == "unknown") && len(history[0].NativeTools.Calls) != 1 {
					t.Fatal("saved observation lost")
				}
			}
			if _, err := j.Restore(history, persist); err != nil || len(writes) > 1 {
				t.Fatalf("duplicate recovery writes=%d err=%v", len(writes), err)
			}
			other := j
			other.AgentID = "task-2"
			if _, err := other.Check(); err != nil {
				t.Fatalf("different durable task blocked: %v", err)
			}
		})
	}
}

func TestNativeJournalRecoveryWriteFailureRetainsBarrier(t *testing.T) {
	j := NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "main"}
	if _, err := j.Begin(nil, nil); err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("write rejected")
	if _, err := j.Restore(nil, func(message.Message) error { return writeErr }); !errors.Is(err, writeErr) {
		t.Fatalf("err=%v", err)
	}
	if _, err := j.Check(); err == nil {
		t.Fatal("failed persistence released barrier")
	}
}
