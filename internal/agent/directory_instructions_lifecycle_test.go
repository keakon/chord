package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestDirectoryInstructionTurnCancelRetainsBindingSnapshot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "AGENTS.md")
	if err = os.WriteFile(path, []byte("original rules"), 0644); err != nil {
		t.Fatal(err)
	}
	turn, cancel := context.WithCancel(context.Background())
	r, _, err := prepareDirectoryInstructions(turn, directoryInstructionBinding{ContentRoot: root, WorkDir: root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.cancel()
	cancel()
	if err = os.WriteFile(path, []byte("changed rules"), 0644); err != nil {
		t.Fatal(err)
	}
	d, err := r.Discover(context.Background(), r.binding, []tools.InstructionTarget{{Path: root, Mode: tools.InstructionTargetDirectory}})
	if err != nil || d.Observations["."].Body != "original rules" {
		t.Fatalf("cancel changed frozen rules: %+v %v", d, err)
	}
}

// A rule file that cannot be loaded is ignored, exactly like a missing one.
// The harness never judges or enforces rule content, so a failure may not fail
// the session, gate a tool, or reach the model.
func TestDirectoryInstructionUnreadableRulesAreIgnored(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A directory named AGENTS.md cannot be opened as a regular file, which is
	// the only kind of failure left: no rule content, no ruleset, no gate.
	if err = os.Mkdir(filepath.Join(root, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, facts, err := prepareDirectoryInstructions(t.Context(), directoryInstructionBinding{ContentRoot: root, WorkDir: root}, nil)
	if err != nil {
		t.Fatalf("unreadable rules failed preparation: %v", err)
	}
	defer r.cancel()
	if len(facts) != 0 {
		t.Fatalf("unreadable rules produced facts: %+v", facts)
	}
	if v := buildDirectoryInstructionView(r.initial, nil, r.binding); len(v.Initial) != 0 || len(v.Dynamic) != 0 {
		t.Fatalf("unreadable rules reached the view: %+v", v)
	}
}

func TestDirectoryInstructionActivationRetryNeedsRealImprovement(t *testing.T) {
	history := []message.Message{{Role: message.RoleUser, Content: "task"}}
	key := directoryInstructionActivationKey(history, "provider/model", 8192)
	diagnostic := append(append([]message.Message(nil), history...), message.Message{Role: message.RoleUser, Kind: message.KindDirectoryInstructionProjection, Content: "blocked scope"})
	if directoryInstructionActivationKey(diagnostic, "provider/model", 8192) != key {
		t.Fatal("diagnostic growth rearmed activation")
	}
	changes := [][]message.Message{append(append([]message.Message(nil), history...), message.Message{Role: message.RoleUser, Content: "task"}), {{Role: message.RoleUser, IsCompactionSummary: true, Content: "checkpoint", CompactionSkillActivation: &message.SkillActivationCarry{ContextEpoch: 1}}}}
	for _, changed := range changes {
		if directoryInstructionActivationKey(changed, "provider/model", 8192) == key {
			t.Fatal("new user/compaction did not rearm")
		}
	}
	if directoryInstructionActivationKey(history, "provider/other", 8192) == key || directoryInstructionActivationKey(history, "provider/model", 16384) == key {
		t.Fatal("model/budget change did not rearm")
	}
}
func TestDirectoryInstructionMalformedRestoreFailsPreparation(t *testing.T) {
	root := t.TempDir()
	_, _, err := prepareDirectoryInstructions(context.Background(), directoryInstructionBinding{ContentRoot: root, WorkDir: root}, []message.Message{{Role: message.RoleUser, Kind: message.KindDirectoryInstructions}})
	if err == nil {
		t.Fatal("malformed durable fact allowed preparation")
	}
}

func prepareSubDirectoryInstructionsForTest(t *testing.T, s *SubAgent) {
	t.Helper()
	// This helper installs a fixture outside the owner loop. Stop and join an
	// already-running worker so its request preparation cannot replace it.
	if s.started.Load() {
		s.cancel()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := s.waitDone(ctx); err != nil {
			t.Fatalf("stop worker before installing instructions: %v", err)
		}
	}
	root := s.effectiveToolBaseDir()
	if s.parent != nil {
		root = s.parent.ContentRoot()
	}
	r, _, err := prepareDirectoryInstructions(t.Context(), directoryInstructionBinding{ContentRoot: root, WorkDir: s.effectiveToolBaseDir(), AgentID: s.taskID}, s.ctxMgr.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.cancel)
	s.directoryInstructions.Store(r)
	s.refreshSessionContextReminder()
}
