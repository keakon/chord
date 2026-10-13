package message

import "testing"

func TestDeliveryIdentityCloneAndRepeatedCheckpoint(t *testing.T) {
	a := DeliverySource{SessionID: "ab", TaskID: "c", ToolCallID: "tool"}
	b := DeliverySource{SessionID: "a", TaskID: "bc", ToolCallID: "tool"}
	if DeliveryID(a) == DeliveryID(b) {
		t.Fatal("ambiguous identity encoding")
	}
	file := DeliveredFile{ID: DeliveryID(a), Source: a, Locator: FileLocator{Kind: FileLocatorWorkspace, Workspace: &WorkspaceFileLocator{Root: "/root", Path: "/root/report"}}}
	m := Message{DeliveredFiles: []DeliveredFile{file}}
	clone := m.Clone()
	clone.DeliveredFiles[0].Locator.Workspace.Path = "changed"
	if m.DeliveredFiles[0].Locator.Workspace.Path != "/root/report" {
		t.Fatal("clone aliases canonical locator")
	}
	checkpoint := Message{CompactionDeliveredFiles: CollectDeliveredFiles([]Message{m, m})}
	next := CollectDeliveredFiles([]Message{checkpoint, m})
	if len(next) != 1 || next[0].ID != file.ID {
		t.Fatal("checkpoint lost or duplicated delivery")
	}
}
