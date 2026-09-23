package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestCompactionStateFilesPrecedeKeyFilesUnderBudget(t *testing.T) {
	projectRoot := t.TempDir()
	notesPath := filepath.Join(projectRoot, ".chord", "notes", "resume.md")
	if err := os.MkdirAll(filepath.Dir(notesPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notesPath, []byte("## Resume\nNext: verify the parser.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "source.go"), []byte(strings.Repeat("source body\n", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := newTestMainAgent(t, projectRoot)
	agent.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	summary := "## Files and Evidence\n- source.go\n\n## Externalized State\n- .chord/notes/resume.md\n- source.go\n"
	paths := agent.compactionContinuationFiles(summary)
	if !slices.Equal(paths, []string{".chord/notes/resume.md", "source.go"}) {
		t.Fatalf("paths = %v", paths)
	}
	result := filectx.BuildFilePartsWithOptions(paths, agent.resolveCheckpointFilePath, filectx.BuildFilePartsOptions{MaxFileBytes: 64, MaxTotalBytes: 64})
	found := false
	for _, part := range result.Parts {
		found = found || strings.Contains(part.Text, "Next: verify the parser.")
	}
	if !found {
		t.Fatalf("notes lost under file budget: %+v", result)
	}
}

// The Externalized State section is the only place a checkpoint records the
// files the continuation must re-read, so the extractor must accept the
// agent-owned .chord/notes and .chord/plans documents while keeping the rest
// of the .chord/ tree — memory records, session archives — filtered as
// harness-internal, and it must never surface the section's own prose.
func TestExtractCompactionStateFilesScopesChordRoots(t *testing.T) {
	projectRoot := t.TempDir()
	for _, rel := range []string{
		".chord/notes/task.md",
		".chord/plans/20260914-plan.md",
		".chord/memory/records/rec.md",
		".chord/sessions/s1/main.jsonl",
		".Chord/notes/UPPER.md",
		".Chord/memory/records/upper.md",
		".Chord/sessions/s1/main.jsonl",
		"src/main.go",
	} {
		abs := filepath.Join(projectRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte("state\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	section := strings.Join([]string{
		"## Externalized State",
		"- Model-declared references only; existence is not verified. Use the read tool to load any path before relying on it:",
		"- .chord/notes/task.md",
		"- .chord/plans/20260914-plan.md",
		"- .chord/memory/records/rec.md",
		"- .chord/sessions/s1/main.jsonl",
		"- src/main.go",
		"- src/missing.go",
		"- ../outside.go",
		"- .Chord/notes/UPPER.md",
		"- .Chord/memory/records/upper.md",
		"- .Chord/sessions/s1/main.jsonl",
		"",
		"## Next Step",
		"- continue",
	}, "\n")

	got := extractCompactionStateFiles(section, projectRoot)
	// The uppercase spellings exist on disk, so a verdict of "filtered" here
	// comes from the path rule and not from a failed stat: an APFS or NTFS
	// host opens .Chord/memory/... as the harness-internal file it is.
	want := []string{".chord/notes/task.md", ".chord/plans/20260914-plan.md", "src/main.go", ".Chord/notes/UPPER.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("extractCompactionStateFiles = %v, want %v", got, want)
	}

	// The key-file list keeps rejecting every .chord/ path: notes roots are
	// admitted for the state_files contract only.
	keySection := strings.Replace(section, "## Externalized State", "## Files and Evidence", 1)
	if got := extractCompactionKeyFiles(keySection, projectRoot); !slices.Equal(got, []string{"src/main.go"}) {
		t.Fatalf("extractCompactionKeyFiles = %v, want [src/main.go]", got)
	}
}

func TestExtractCompactionStateFilesRejectsExternalSymlink(t *testing.T) {
	projectRoot := t.TempDir()
	external := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(external, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(projectRoot, "state.md")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := extractCompactionStateFiles("## Externalized State\n- state.md\n", projectRoot); len(got) != 0 {
		t.Fatalf("external symlink was accepted: %v", got)
	}
}

// A symlink that stays inside the project must still load. The confined read
// opens the resolved location because os.Root refuses to follow a symlink whose
// target is absolute, even when that target points back into the root.
func TestCompactionContinuationFilesLoadsInRootAbsoluteSymlink(t *testing.T) {
	projectRoot := t.TempDir()
	real := filepath.Join(projectRoot, "real.md")
	if err := os.WriteFile(real, []byte("state body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(projectRoot, "state.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 0)
	summary := "## Externalized State\n- state.md\n"

	out, insertedAt := a.injectCompactionFileContext([]message.Message{
		{Role: "user", IsCompactionSummary: true, Content: summary},
	})
	if insertedAt != 1 || len(out) != 2 {
		t.Fatalf("an in-root symlink must still load, insertedAt=%d len=%d", insertedAt, len(out))
	}
	carried := false
	for _, part := range out[1].Parts[1:] {
		carried = carried || strings.Contains(part.Text, "state body")
	}
	if !carried {
		t.Fatalf("resolved file body missing from overlay: %+v", out[1].Parts)
	}
}

// The confined read must refuse a symlink that leaves the root even when the
// path was accepted while it was still a regular file: the request-time load
// re-resolves it, and os.Root rejects the swap.
func TestCheckpointReadConfinementRejectsSwappedSymlink(t *testing.T) {
	projectRoot := t.TempDir()
	target := filepath.Join(projectRoot, "state.md")
	if err := os.WriteFile(target, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := extractCompactionStateFiles("## Externalized State\n- state.md\n", projectRoot); !slices.Equal(got, []string{"state.md"}) {
		t.Fatalf("regular file must pass extraction, got %v", got)
	}
	a := newTestMainAgent(t, projectRoot)

	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got := a.resolveCheckpointFileReadPath("state.md"); got != "" {
		t.Fatalf("resolveCheckpointFileReadPath accepted an out-of-root symlink: %q", got)
	}
	if _, err := a.readCheckpointFile("state.md"); err == nil {
		t.Fatal("readCheckpointFile followed a symlink out of the project root")
	}
}

// Re-loading a model-declared state file must never widen what the model could
// already reach: the read permission rule has to resolve to allow. An ask rule
// in particular must not be silently auto-approved by the overlay. The file
// tracker is deliberately not consulted — it only carries the surviving
// transcript, so an archived checkpoint's read records are gone after a restore
// and gating on it would silently disable the overlay.
func TestCompactionContinuationFilesGateStateFilesByReadPermission(t *testing.T) {
	projectRoot := t.TempDir()
	notesAbs := filepath.Join(projectRoot, ".chord", "notes", "task.md")
	if err := os.MkdirAll(filepath.Dir(notesAbs), 0o755); err != nil {
		t.Fatalf("mkdir notes dir: %v", err)
	}
	if err := os.WriteFile(notesAbs, []byte("# objective\nship it\n"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 0)
	summary := "## Externalized State\n- .chord/notes/task.md\n\n## Next Step\n- continue"

	// A restore rebuilds the file tracker from the surviving transcript only,
	// so a checkpoint that survived an archive has no read record here. The
	// read rule still allows the path, so the overlay must stay available.
	if got := a.compactionContinuationFiles(summary); !slices.Equal(got, []string{".chord/notes/task.md"}) {
		t.Fatalf("a read-allowed declared file must be re-loaded independently of the tracker, got %v", got)
	}

	// Wiring: the declared file reaches the request-local overlay, with the
	// notes body carried by the file reference parts.
	out, insertedAt := a.injectCompactionFileContext([]message.Message{
		{Role: "user", IsCompactionSummary: true, Content: summary},
	})
	if insertedAt != 1 || len(out) != 2 {
		t.Fatalf("expected one overlay after the checkpoint, insertedAt=%d len=%d", insertedAt, len(out))
	}
	if !strings.Contains(out[1].Parts[0].Text, compactionFileCtxPrefix) {
		t.Fatalf("overlay must carry the reload marker, got %q", out[1].Parts[0].Text)
	}
	carried := false
	for _, part := range out[1].Parts[1:] {
		carried = carried || strings.Contains(part.Text, "ship it")
	}
	if !carried {
		t.Fatalf("observed notes head must be part of the injected overlay: %+v", out[1].Parts)
	}

	// Later rules win: a read deny (or ask) on the path overrides the allow-all.
	for name, action := range map[string]permission.Action{
		"deny": permission.ActionDeny,
		"ask":  permission.ActionAsk,
	} {
		t.Run(name, func(t *testing.T) {
			a.ruleset = permission.Ruleset{
				{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
				{Permission: tools.NameRead, Pattern: ".chord/notes/*", Action: action},
			}
			if got := a.compactionContinuationFiles(summary); len(got) != 0 {
				t.Fatalf("read %s must keep the file out of the overlay, got %v", name, got)
			}
		})
	}
}

// A path the read rule rejects must stay rejected even when the checkpoint also
// lists it under Files and Evidence: the summary-derived key-file pass shares
// the same gate instead of re-adding what the declared pass just dropped.
func TestCompactionContinuationFilesRejectedDeclaredPathIsNotReaddedByKeyFiles(t *testing.T) {
	projectRoot := t.TempDir()
	notesAbs := filepath.Join(projectRoot, ".chord", "notes", "task.md")
	if err := os.MkdirAll(filepath.Dir(notesAbs), 0o755); err != nil {
		t.Fatalf("mkdir notes dir: %v", err)
	}
	if err := os.WriteFile(notesAbs, []byte("# objective\nship it\n"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameRead, Pattern: ".chord/notes/*", Action: permission.ActionDeny},
	}
	summary := "## Externalized State\n- .chord/notes/task.md\n\n## Files and Evidence\n- .chord/notes/task.md\n\n## Next Step\n- continue"
	if got := a.compactionContinuationFiles(summary); len(got) != 0 {
		t.Fatalf("a rejected declared path must not be re-added through the key-file pass, got %v", got)
	}
}

// A key file the summary names under Files and Evidence only is still an
// auto-loaded path, so it obeys the same read rule as a declared state file.
func TestCompactionContinuationFilesGateKeyFilesByReadPermission(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "key.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameRead, Pattern: "*.go", Action: permission.ActionAsk},
	}
	summary := "## Files and Evidence\n- key.go\n\n## Next Step\n- continue"
	if got := a.compactionContinuationFiles(summary); len(got) != 0 {
		t.Fatalf("a key file whose read rule asks must not be auto-loaded, got %v", got)
	}
}

// Under YOLO the execution gate bypasses ordinary tools outright, so every
// path is already reachable and the filtered ruleset's missing read rules must
// not report a false deny: the overlay has to stay available or the flagship
// state-file re-load silently no-ops in the mode long autonomous sessions use.
func TestCompactionContinuationFilesInjectUnderYolo(t *testing.T) {
	projectRoot := t.TempDir()
	notesAbs := filepath.Join(projectRoot, ".chord", "notes", "task.md")
	if err := os.MkdirAll(filepath.Dir(notesAbs), 0o755); err != nil {
		t.Fatalf("mkdir notes dir: %v", err)
	}
	if err := os.WriteFile(notesAbs, []byte("# objective\nship it\n"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.SetInitialYoloMode(true)
	// Deliberately no rules at all: the pre-fix gate evaluated the YOLO-
	// filtered ruleset, found no read rules, and re-loaded nothing.
	summary := "## Externalized State\n- .chord/notes/task.md\n\n## Next Step\n- continue"
	if got := a.compactionContinuationFiles(summary); !slices.Equal(got, []string{".chord/notes/task.md"}) {
		t.Fatalf("YOLO must mirror the execution bypass for state-file re-loads, got %v", got)
	}

	// An explicit wildcard allow plus a read deny still bypasses: YOLO never
	// evaluates ordinary tools, and this must match the execution gate.
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameRead, Pattern: ".chord/notes/*", Action: permission.ActionDeny},
	}
	if got := a.compactionContinuationFiles(summary); !slices.Equal(got, []string{".chord/notes/task.md"}) {
		t.Fatalf("YOLO bypass must ignore ordinary read rules, got %v", got)
	}
}
