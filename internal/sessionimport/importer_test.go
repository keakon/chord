package sessionimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/recovery"
)

func TestImport_OpenCode_WritesRecoverableSession(t *testing.T) {
	stateDir := t.TempDir()
	// Import path policy uses env overrides.
	t.Setenv("CHORD_STATE_DIR", stateDir)
	// Ensure sessions dir follows state dir.
	t.Setenv("CHORD_SESSIONS_DIR", "")

	projectRoot := t.TempDir()
	input := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(input, []byte(`{"info":{"id":"sess-1"},"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	res, err := Import(context.Background(), ImportOptions{Source: "opencode", InputPath: input, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.SessionID == "" || res.SessionDir == "" {
		t.Fatalf("missing session fields: %+v", res)
	}

	// Ensure recovery can read it.
	rm := recovery.NewRecoveryManager(res.SessionDir)
	msgs, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}

	// Ensure session-meta has import provenance.
	meta, err := recovery.LoadSessionMeta(res.SessionDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta == nil || meta.ImportedFrom == nil || meta.ImportedFrom.Source != "opencode" {
		t.Fatalf("ImportedFrom missing: %+v", meta)
	}

	// Ensure import-report exists and is valid JSON.
	data, err := os.ReadFile(filepath.Join(res.SessionDir, "import-report.json"))
	if err != nil {
		t.Fatalf("read import-report: %v", err)
	}
	var rep ImportReport
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("parse import-report: %v", err)
	}
	if rep.Source != "opencode" {
		t.Fatalf("rep.Source=%q", rep.Source)
	}

	// Ensure it shows up in session list (main.jsonl non-empty).
	locator, err := config.DefaultPathLocator()
	if err != nil {
		t.Fatalf("DefaultPathLocator: %v", err)
	}
	pl, err := locator.LocateProject(projectRoot)
	if err != nil {
		t.Fatalf("LocateProject: %v", err)
	}
	list, err := recovery.ListSessions(pl.ProjectSessionsDir, "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range list {
		if s.ID == res.SessionID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("imported session not found in ListSessions")
	}
}
