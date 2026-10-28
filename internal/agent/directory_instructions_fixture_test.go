package agent

import (
	"context"
	"testing"
)

// These fixtures call request handlers without running the owner event loop.
// Prepare through the real resolver synchronously so the test begins at the
// same prepared boundary an event-loop request would reach asynchronously.
func prepareMainRequestDirectoryFixture(t *testing.T, a *MainAgent) {
	t.Helper()
	binding := directoryInstructionBinding{SessionEpoch: a.sessionEpoch, AgentID: "main", Generation: a.workDirState.load().Generation, ContentRoot: a.ContentRoot(), WorkDir: a.effectiveToolBaseDir()}
	r, facts, err := prepareDirectoryInstructions(context.Background(), binding, a.ctxMgr.Snapshot(), nil)
	if err != nil {
		t.Fatalf("prepare directory fixture: %v", err)
	}
	if len(facts) > 0 {
		msg := directoryInstructionFactMessage(facts)
		manager := a.recoveryManager()
		if manager == nil {
			t.Fatal("directory fixture requires durable manager")
		}
		if err := manager.PersistMessageDurable("main", msg); err != nil {
			t.Fatal(err)
		}
		a.ctxMgr.Append(msg)
	}
	if old := a.directoryInstructions.Swap(r); old != nil {
		old.cancel()
	}
	t.Cleanup(r.cancel)
}

func prepareSubRequestDirectoryFixture(t *testing.T, s *SubAgent) {
	t.Helper()
	root := s.effectiveToolBaseDir()
	if s.parent != nil {
		root = s.parent.ContentRoot()
	}
	binding := directoryInstructionBinding{SessionEpoch: s.sessionEpoch, AgentID: s.taskID, Generation: s.workDirState.load().Generation, ContentRoot: root, WorkDir: s.effectiveToolBaseDir()}
	r, facts, err := prepareDirectoryInstructions(context.Background(), binding, s.ctxMgr.Snapshot(), nil)
	if err != nil {
		t.Fatalf("prepare worker directory fixture: %v", err)
	}
	if len(facts) > 0 {
		msg := directoryInstructionFactMessage(facts)
		manager := s.recoveryManager()
		if manager == nil {
			t.Fatal("worker directory fixture requires durable manager")
		}
		if err := manager.PersistMessageDurable(s.instanceID, msg); err != nil {
			t.Fatal(err)
		}
		s.ctxMgr.Append(msg)
	}
	if old := s.directoryInstructions.Swap(r); old != nil {
		old.cancel()
	}
	t.Cleanup(r.cancel)
}
