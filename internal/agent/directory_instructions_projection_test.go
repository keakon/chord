package agent

import (
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestDirectoryInstructionProjectionNoopOwnership(t *testing.T) {
	history := make([]message.Message, 1, 4)
	history[0] = message.Message{Role: message.RoleUser, Content: "sample"}
	r := &directoryInstructionRuntime{}
	for range 2 {
		got, err := projectDirectoryInstructionHistory(history, r)
		if err != nil || len(got) != 1 || &got[0] != &history[0] {
			t.Fatalf("no-op projection: %+v %v", got, err)
		}
		if cap(got) != len(got) {
			t.Fatal("append could overwrite shared snapshot storage")
		}
		if r.view.Load() == nil {
			t.Fatal("no-op omitted runtime view")
		}
	}
	r.view.Store(&directoryInstructionView{Err: fmt.Errorf("sample invalid view")})
	if _, err := projectDirectoryInstructionHistory(history, r); err == nil {
		t.Fatal("no-op ignored invalid view")
	}
}

// A scope whose rules could not be loaded is simply absent; it must never turn
// into a request message, because the model cannot act on the reason and would
// only be told about a condition the harness has to resolve.
func TestDirectoryInstructionProjectionOmitsBlockers(t *testing.T) {
	r := &directoryInstructionRuntime{}
	r.view.Store(&directoryInstructionView{Blocked: []message.DirectoryInstructionFact{{Scope: "sample", Reason: message.DirectoryInstructionReasonIO, Detail: "cannot read rule file"}}})
	history := []message.Message{{Role: message.RoleUser, Content: "sample"}}
	got, err := projectDirectoryInstructionHistory(history, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "sample" {
		t.Fatalf("blocker reached the request: %+v", got)
	}
}

func TestDirectoryInstructionProjectionPreservesPreparedReferences(t *testing.T) {
	fact := directoryTestFact("sample", "sample rules")
	r := &directoryInstructionRuntime{}
	r.view.Store(&directoryInstructionView{Dynamic: []message.DirectoryInstructionFact{fact}})
	history := []message.Message{directoryInstructionProjection([]message.DirectoryInstructionFact{fact})}
	got, err := projectDirectoryInstructionHistory(history, r)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := validateDirectoryInstructionRequest(got, *r.view.Load())
	if err != nil || len(visible) != 1 {
		t.Fatalf("prepared references lost: %+v %v", visible, err)
	}
}
