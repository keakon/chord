package agent

import "testing"

func TestResolveResumeSessionPathRejectsDeletingAndNestedID(t *testing.T) {
	root := t.TempDir()
	current := testProjectSessionDir(t, root, "current")
	tombstone := testProjectSessionDir(t, root, "session.deleting")
	persistRestorableSession(t, tombstone)
	a := newTestMainAgentForRestore(t, root, current)
	for _, id := range []string{"session.deleting", "session.deleting/", "session.deleting/.", "../session", "."} {
		if path, err := a.resolveResumeSessionPath(id); err == nil {
			t.Fatalf("accepted invalid resume ID %q as %s", id, path)
		}
	}
}
