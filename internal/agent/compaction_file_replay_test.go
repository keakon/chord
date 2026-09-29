package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
)

func TestCompactionFileReplayPreservesPrefixAndLatestVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("initial findings")
	a := newTestMainAgent(t, root)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 0)
	checkpoint := message.Message{Role: message.RoleUser, IsCompactionSummary: true, Content: "## Files and Evidence\n- notes.md\n", CompactionFileRevisions: captureCompactionFileRevisions([]string{"notes.md"}, a.resolveCheckpointFilePath)}
	raw := []message.Message{checkpoint, {Role: message.RoleUser, Content: "continue"}}
	first, _ := a.injectCompactionFileContext(raw)
	write("new findings supersede initial findings")
	raw = append(raw, message.Message{Role: message.RoleAssistant, RequestBatch: 10, ToolCalls: []message.ToolCall{{ID: "write-1", Name: "write"}}}, message.Message{Role: message.RoleTool, ToolCallID: "write-1", Content: "written"})
	updated, _ := a.injectCompactionFileContext(raw)
	if !reflect.DeepEqual(first, updated[:len(first)]) {
		t.Fatal("notes update rewrote the previous request prefix")
	}
	last := updated[len(updated)-1]
	if !strings.Contains(last.Parts[1].Text, "new findings") || !strings.Contains(last.Parts[1].Text, `changed_since_checkpoint="true"`) {
		t.Fatalf("missing latest snapshot: %+v", last)
	}
	if updated[len(updated)-2].ToolCallID != "write-1" {
		t.Fatal("snapshot split a tool-call/result pair")
	}
	if count := compactionFileContextPrefixCount(updated, len(raw)); count != 1 {
		t.Fatalf("prefix overlay count = %d, want 1 (exclude tail)", count)
	}
	raw = append(raw, message.Message{Role: message.RoleUser, Content: "next"})
	stable, _ := a.injectCompactionFileContext(raw)
	if !reflect.DeepEqual(updated, stable[:len(updated)]) {
		t.Fatal("unchanged snapshot moved or was duplicated")
	}
	if count := compactionFileContextPrefixCount(stable, len(raw)); count != 2 {
		t.Fatalf("prefix overlay count = %d, want 2", count)
	}
	// A reduced result keeps the same identity, so the version boundary survives.
	raw[3].Content = "reduced tool result"
	reduced, _ := a.injectCompactionFileContext(raw)
	if len(reduced) != len(stable) || !reflect.DeepEqual(reduced[5], stable[5]) {
		t.Fatal("reduction moved the snapshot boundary")
	}
	// Request consumers must not mutate the cached snapshot for the next call.
	stable[1].Parts[1].Text = "caller mutation"
	again, _ := a.injectCompactionFileContext(raw)
	if strings.Contains(again[1].Parts[1].Text, "caller mutation") {
		t.Fatal("request aliased cached snapshot")
	}
	// Rewinding removes the update's anchor. Restore must load the current disk
	// state instead of placing a version at an unrelated history boundary.
	rewound, _ := a.injectCompactionFileContext(raw[:2])
	if len(rewound) != 3 || !strings.Contains(rewound[1].Parts[1].Text, "new findings") {
		t.Fatal("rewind retained obsolete version positions")
	}
	a.compactionFiles.reset()
	resumed, _ := a.injectCompactionFileContext(raw[:2])
	if !reflect.DeepEqual(rewound, resumed) {
		t.Fatal("fresh runtime did not restore the latest file state")
	}
}

func TestCompactionFileReplayRevocationAndDeletion(t *testing.T) {
	for _, action := range []string{"deny", "ask", "delete", "escape"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "notes.md")
			if err := os.WriteFile(path, []byte("private notes"), 0600); err != nil {
				t.Fatal(err)
			}
			a := newTestMainAgent(t, root)
			a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
			a.ctxMgr.SetTokenBudgets(120000, 120000, 0)
			raw := []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: "## Files and Evidence\n- notes.md\n"}}
			first, _ := a.injectCompactionFileContext(raw)
			if len(first) != 2 {
				t.Fatal("missing initial snapshot")
			}
			switch action {
			case "deny":
				a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}}
			case "ask":
				a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAsk}}
			default:
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if action == "escape" {
					target := filepath.Join(t.TempDir(), "secret")
					if err := os.WriteFile(target, []byte("outside secret"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, idx := a.injectCompactionFileContext(raw)
			if idx != -1 || !reflect.DeepEqual(got, raw) || len(a.compactionFiles.versions) != 0 {
				t.Fatal("unavailable file survived in cached replay")
			}
		})
	}
}

func TestCompactionFileReplayBoundsAndCheckpointReset(t *testing.T) {
	var r compactionFileReplay
	raw := []message.Message{{Role: message.RoleUser, Content: "checkpoint", IsCompactionSummary: true}}
	snapshot := func(text string) message.Message {
		return message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Parts: []message.ContentPart{{Type: message.ContentPartText, Text: compactionFileCtxPrefix}, {Type: message.ContentPartText, Text: `<file path="notes.md">` + text + `</file>`}}}
	}
	r.replay(raw, 0, "checkpoint", "/root", snapshot("old"), 10, 25)
	raw = append(raw, message.Message{Role: message.RoleUser, Content: "next"})
	r.replay(raw, 0, "checkpoint", "/root", snapshot("new"), 10, 25)
	if len(r.versions) != 2 {
		t.Fatal("expected two versions")
	}
	out, _ := r.replay(raw, 0, "checkpoint", "/root", snapshot("latest"), 10, 25)
	if len(r.versions) != 1 || !strings.Contains(out[1].Parts[1].Text, "latest") {
		t.Fatal("budget did not replace history with current snapshot")
	}
	r.replay(raw, 0, "next checkpoint", "/root", snapshot("latest"), 10, 25)
	if len(r.versions) != 1 || r.checkpoint != "next checkpoint" {
		t.Fatal("checkpoint retained prior replay state")
	}
	r.replay(raw, 0, "next checkpoint", "/other-root", snapshot("other workspace"), 10, 25)
	if len(r.versions) != 1 || r.root != "/other-root" {
		t.Fatal("workspace change retained prior replay state")
	}
	for i := range 20 {
		r.replay(raw, 0, "next checkpoint", "/other-root", snapshot(strings.Repeat("x", i+1)), 1, 10000)
		if len(r.versions) > 8 {
			t.Fatal("unbounded snapshot versions")
		}
	}
}
