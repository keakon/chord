package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestDirectoryInstructionCoverageRequiresFrozenTargetAndRequestEvidence(t *testing.T) {
	r, root := newResolverFixture(t)
	writeResolverRule(t, root, "root rule")
	target := tools.InstructionTarget{Path: filepath.Join(root, "file"), Mode: tools.InstructionTargetEntry}
	request := directoryInstructionRequest{Binding: r.binding, Visible: map[directoryInstructionVersion]struct{}{}}
	p := toolExecutionPipeline{directoryRuntime: r, directoryRequest: &request, directoryGate: &directoryInstructionGate{}, toolBaseDir: root}
	call := message.ToolCall{ID: "call-1", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"file","content":"sample"}`)}
	if p.directoryInstructionsCovered(call) {
		t.Fatal("unknown target covered")
	}
	d, err := r.Discover(context.Background(), r.binding, []tools.InstructionTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	if p.directoryInstructionsCovered(call) {
		t.Fatal("unseen rule covered")
	}
	request.Visible[directoryInstructionFactFromObservation(r.binding, ".", d.Observations["."]).Ref()] = struct{}{}
	if !p.directoryInstructionsCovered(call) {
		t.Fatal("visible frozen target not covered")
	}
	if err := os.Remove(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if !p.directoryInstructionsCovered(call) {
		t.Fatal("coverage performed filesystem rediscovery")
	}
	p.directoryGate.Close(directoryInstructionStopRequired)
	if p.directoryInstructionsCovered(call) {
		t.Fatal("closed gate covered")
	}
}
func TestDirectoryInstructionCoverageUnknownPrefixStopsSuffix(t *testing.T) {
	turn := &Turn{}
	turn.recordStreamingToolCall(PendingToolCall{CallID: "first", Name: tools.NameRead, ArgsJSON: `{"path":"unknown"}`})
	turn.recordStreamingToolCall(PendingToolCall{CallID: "second", Name: tools.NameRead, ArgsJSON: `{"path":"known"}`})
	reducer := streamToolDeltaReducer{turn: turn, directoryCovered: func(call message.ToolCall) bool { return call.ID != "first" }}
	if reducer.directorySpeculativePrefixCovered("second", tools.NameRead, `{"path":"known"}`) {
		t.Fatal("unknown prefix did not stop suffix")
	}
}

func TestDirectoryInstructionCoverageNewEntryUnderFrozenRoot(t *testing.T) {
	r, root := newResolverFixture(t)
	discoverResolverDirectory(t, r, root)
	request := directoryInstructionRequest{Binding: r.binding, Visible: map[directoryInstructionVersion]struct{}{}}
	p := toolExecutionPipeline{directoryRuntime: r, directoryRequest: &request, directoryGate: &directoryInstructionGate{}, toolBaseDir: root}
	call := message.ToolCall{ID: "new-file", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"new.txt","content":"sample"}`)}
	if !p.directoryInstructionsCovered(call) {
		t.Fatal("new root entry unnecessarily lost speculative eligibility")
	}
	call.Args = json.RawMessage(`{"path":"unknown/new.txt","content":"sample"}`)
	if p.directoryInstructionsCovered(call) {
		t.Fatal("unknown parent chain covered")
	}
}
