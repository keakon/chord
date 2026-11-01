package main

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func TestHeadlessAgentDonePushesCompletionReport(t *testing.T) {
	event := agent.AgentDoneEvent{
		TaskID: "task-1", Summary: "Updated files; formatting passed.",
		Message: "Updated files; formatting passed.\n- actual_files_changed: observed.go\n- reported_files_changed: reported.go\n- remaining_limitations: Integration tests were not run.\n- file_attribution_incomplete: true",
	}
	envelopes := filterHeadlessEvent(event, &headlessState{})
	if len(envelopes) != 1 {
		t.Fatalf("envelopes = %#v", envelopes)
	}
	data, err := json.Marshal(envelopes[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]json.RawMessage
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	var report string
	if err := json.Unmarshal(restored["message"], &report); err != nil {
		t.Fatal(err)
	}
	if report != event.Message {
		t.Fatalf("headless dropped completion report: %s", data)
	}
	for _, field := range []string{"completion", "verification"} {
		if _, ok := restored[field]; ok {
			t.Fatalf("headless added %s: %s", field, data)
		}
	}
}
