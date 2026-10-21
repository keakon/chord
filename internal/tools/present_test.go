package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
)

func TestPresentAtomicBatchAndDeduplication(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	sink := &DeliveryCollector{}
	ctx := WithDeliveryCollector(WithSessionDir(context.Background(), t.TempDir()), sink)
	tool := PresentTool{BaseDir: dir}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"files":[{"path":"report.md"},{"path":"missing"}]}`)); err == nil {
		t.Fatal("missing file accepted")
	}
	if len(sink.Drain()) != 0 {
		t.Fatal("partial delivery leaked")
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"files":[{"path":"report.md","description":"first"},{"path":"./report.md","description":"last"}]}`)); err != nil {
		t.Fatal(err)
	}
	files := sink.Drain()
	expected, _ := filepath.EvalSymlinks(filepath.Join(dir, "report.md"))
	if len(files) != 1 || files[0].Description != "last" || files[0].Locator.Workspace.Path != expected {
		t.Fatalf("bad delivery: %#v", files)
	}
}
func TestPresentStrictInputs(t *testing.T) {
	for _, raw := range []string{`{}`, `{"files":[]}`, `{"files":[{}]}`, `{"files":[{"path":"x","artifact":{}}]}`, `{"files":[{"artifact":{"rel_path":"artifacts/x","sha256":"00"}}]}`, `{"files":[{"path":"x","command":"open"}]}`, `{"files":[{"path":"x"}],"id":"model-controlled"}`} {
		if _, err := ParsePresentArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if (PresentTool{}).IsReadOnly() {
		t.Fatal("present is not read only")
	}
	if _, err := (PresentTool{}).Execute(context.Background(), json.RawMessage(`{"files":[{"path":"x"}]}`)); err == nil {
		t.Fatal("nonpersistent call accepted")
	}
}

func TestIsChordInternalDeliveryTarget(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")
	workspace := func(path string) message.FileLocator {
		return message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Root: root, Path: path}}
	}
	inRoot := func(elem ...string) string { return filepath.Join(append([]string{root}, elem...)...) }
	artifact := message.FileLocator{Kind: message.FileLocatorArtifact, Artifact: &message.ArtifactFileLocator{RelPath: "artifacts/report.md"}}
	for _, tc := range []struct {
		name string
		loc  message.FileLocator
		want bool
	}{
		{"notes", workspace(inRoot(".chord", "notes", "task.md")), true},
		{"plans", workspace(inRoot(".chord", "plans", "20260101-slug.md")), true},
		{"chord root itself", workspace(inRoot(".chord")), true},
		{"other chord child", workspace(inRoot(".chord", "memory", "records", "r.md")), true},
		{"ordinary file", workspace(inRoot("docs", "guide.md")), false},
		{"chord-like filename", workspace(inRoot("docs", "chord-notes.md")), false},
		{"chord-like dirname", workspace(inRoot("x.chord", "report.md")), false},
		{"artifact", artifact, false},
		{"missing root", message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Path: inRoot(".chord", "notes", "task.md")}}, false},
		// A checkout may live under a .chord directory (worktree.root:
		// .chord/worktrees); where the session works must not decide what it
		// may deliver.
		{"root under .chord", message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Root: inRoot(".chord", "worktrees", "wt"), Path: filepath.Join(root, ".chord", "worktrees", "wt", "docs", "guide.md")}}, false},
		{"root under .chord, own working state", message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Root: inRoot(".chord", "worktrees", "wt"), Path: filepath.Join(root, ".chord", "worktrees", "wt", ".chord", "notes", "task.md")}}, true},
		// Another workspace's working state stays protected on its own path.
		{"outside root", workspace(filepath.Join(root, "..", "other", ".chord", "notes", "task.md")), true},
		{"outside root, ordinary file", workspace(filepath.Join(root, "..", "other", "docs", "guide.md")), false},
	} {
		if got := IsChordInternalDeliveryTarget(tc.loc, permission.PathScope{}); got != tc.want {
			t.Errorf("%s: IsChordInternalDeliveryTarget() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A file inside a known checkout is judged by its path under that checkout's
// root. The main agent's root contains a worktree root below .chord, so
// without the scope the worktree's own location would condemn its ordinary
// files.
func TestIsChordInternalDeliveryTargetUsesContainingCheckout(t *testing.T) {
	mainRoot := filepath.Join(string(filepath.Separator), "repo")
	worktreeRoot := filepath.Join(mainRoot, ".chord", "worktrees", "wt")
	scope := permission.PathScope{Cwd: mainRoot, Roots: []string{worktreeRoot, mainRoot}}
	workspace := func(path string) message.FileLocator {
		return message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Root: mainRoot, Path: path}}
	}
	report := workspace(filepath.Join(worktreeRoot, "report.md"))
	if IsChordInternalDeliveryTarget(report, scope) {
		t.Errorf("worktree's ordinary file judged by the main root's spelling: %#v", report)
	}
	note := workspace(filepath.Join(worktreeRoot, ".chord", "notes", "task.md"))
	if !IsChordInternalDeliveryTarget(note, scope) {
		t.Errorf("worktree's own working state delivered: %#v", note)
	}
	plan := workspace(filepath.Join(mainRoot, ".chord", "plans", "20260101-slug.md"))
	if !IsChordInternalDeliveryTarget(plan, scope) {
		t.Errorf("main checkout's working state delivered: %#v", plan)
	}
}

// A locator stores a symlink-resolved Path beside the working directory as it
// was given, so a root reached through a symlink must still be compared below
// that root. Otherwise a worktree checkout under .chord would condemn the
// ordinary files it delivers.
func TestIsChordInternalDeliveryTargetResolvesSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	docs := filepath.Join(real, ".chord", "worktrees", "wt", "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(docs, "guide.md")
	if err := os.WriteFile(file, []byte("guide"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	loc := message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{
		Root: filepath.Join(link, ".chord", "worktrees", "wt"),
		Path: path,
	}}
	if IsChordInternalDeliveryTarget(loc, permission.PathScope{}) {
		t.Fatalf("worktree checkout under .chord must not make its own docs a .chord target: %#v", loc)
	}
}

func TestPresentDescriptionRejectsEditedRepositoryFiles(t *testing.T) {
	description := (PresentTool{}).Description()
	for _, want := range []string{
		"Use it only when the user needs a file itself",
		"Do not declare repository source, tests, changelog, or configuration files merely because you edited them",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("description missing %q: %q", want, description)
		}
	}
}
