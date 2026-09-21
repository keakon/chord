package skill

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/permission"
)

func scanDiagnosticClean(t *testing.T, loader *Loader) []DiagnosticItem {
	t.Helper()
	items, problems := loader.ScanMetaDiagnostic()
	if len(problems) != 0 {
		t.Fatalf("ScanMetaDiagnostic: unexpected scan problems: %v", problems)
	}
	return items
}

func TestScanMetaDiagnostic_KeepsInvalidRows(t *testing.T) {
	dir := t.TempDir()
	createSkillFile(t, dir, "valid-skill", "Valid skill", "Body\n")
	brokenDir := filepath.Join(dir, "broken")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "SKILL.md"), []byte("---\n: invalid yaml [\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write broken skill: %v", err)
	}
	missingDir := filepath.Join(dir, "nodesc")
	if err := os.MkdirAll(missingDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSkillMD(t, filepath.Join(missingDir, "SKILL.md"), "nodesc-skill", "", "Body\n")

	loader := NewLoader([]string{dir})
	items := scanDiagnosticClean(t, loader)
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3 (one valid, two invalid)", len(items))
	}
	valid := 0
	invalid := 0
	for _, item := range items {
		if item.Err != nil {
			invalid++
			if item.Meta != nil {
				t.Errorf("invalid row should not carry meta: %+v", item)
			}
			if item.Shadowed {
				t.Errorf("invalid row should never be shadowed: %+v", item)
			}
		} else {
			valid++
		}
	}
	if valid != 1 || invalid != 2 {
		t.Fatalf("valid = %d, invalid = %d, want 1 and 2", valid, invalid)
	}
	metas, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("ScanMeta: %v", err)
	}
	if len(metas) != 1 || metas[0].Name != "valid-skill" {
		t.Fatalf("ScanMeta should keep only the valid skill, got %+v", metas)
	}
}

func TestScanMetaDiagnostic_InvalidDoesNotShadow(t *testing.T) {
	highDir := t.TempDir()
	lowDir := t.TempDir()
	invalidDir := filepath.Join(highDir, "shared")
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Same declared name but missing description: invalid, must not occupy it.
	writeSkillMD(t, filepath.Join(invalidDir, "SKILL.md"), "shared", "", "Body\n")
	createSkillFile(t, lowDir, "shared", "Shared skill", "Body\n")

	loader := NewLoader([]string{highDir, lowDir})
	items := scanDiagnosticClean(t, loader)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	var valid *DiagnosticItem
	for i := range items {
		if items[i].Err == nil {
			valid = &items[i]
		}
	}
	if valid == nil {
		t.Fatalf("expected one valid row, got %+v", items)
	}
	if valid.Shadowed {
		t.Fatalf("invalid high-priority file must not shadow the valid skill: %+v", valid)
	}
	metas, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("ScanMeta: %v", err)
	}
	if len(metas) != 1 || metas[0].Name != "shared" {
		t.Fatalf("ScanMeta should expose the low-priority valid skill, got %+v", metas)
	}
}

func TestScanMetaDiagnostic_Shadowed(t *testing.T) {
	highDir := t.TempDir()
	lowDir := t.TempDir()
	createSkillFile(t, highDir, "shared", "High version", "High\n")
	createSkillFile(t, lowDir, "shared", "Low version", "Low\n")

	loader := NewLoader([]string{highDir, lowDir})
	items := scanDiagnosticClean(t, loader)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].Shadowed || items[0].Err != nil {
		t.Fatalf("first row should be the winner: %+v", items[0])
	}
	if !items[1].Shadowed || items[1].Err != nil {
		t.Fatalf("second row should be shadowed: %+v", items[1])
	}
	if items[1].ShadowedBy == "" {
		t.Fatal("shadowed row should record the winner path")
	}
}

func TestLoadMeta_ResourcesParsedAndNormalized(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "doc-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSkillMDWithFM(t, filepath.Join(skillDir, "SKILL.md"), map[string]any{
		"name":        "doc-skill",
		"description": "Doc skill",
		"resources":   []string{"./references/a.md", "references//b.md"},
	}, "Body\n")
	meta, err := LoadMeta(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if len(meta.Resources) != 2 || meta.Resources[0] != "references/a.md" || meta.Resources[1] != "references/b.md" {
		t.Fatalf("resources = %q, want normalized slash paths", meta.Resources)
	}
}

func TestLoadMeta_ResourcesSidecarReplaces(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "sidecar-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSkillMDWithFM(t, filepath.Join(skillDir, "SKILL.md"), map[string]any{
		"name":        "sidecar-skill",
		"description": "Sidecar skill",
		"resources":   []string{"references/old.md"},
	}, "Body\n")
	if err := os.WriteFile(filepath.Join(skillDir, "chord.yaml"), []byte("resources:\n  - references/new.md\n"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	meta, err := LoadMeta(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if len(meta.Resources) != 1 || meta.Resources[0] != "references/new.md" {
		t.Fatalf("sidecar should replace resources, got %q", meta.Resources)
	}

	// An empty sidecar list means "no override", matching the other list fields.
	if err := os.WriteFile(filepath.Join(skillDir, "chord.yaml"), []byte("resources: []\n"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	meta, err = LoadMeta(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if len(meta.Resources) != 1 || meta.Resources[0] != "references/old.md" {
		t.Fatalf("empty sidecar list should not clear resources, got %q", meta.Resources)
	}
}

func TestDigestSkillMetas_ResourcesChangeDigest(t *testing.T) {
	base := &Meta{Name: "s", Description: "d", Location: "/l", RootDir: "/r", Resources: []string{"references/a.md"}}
	changed := &Meta{Name: "s", Description: "d", Location: "/l", RootDir: "/r", Resources: []string{"references/b.md"}}
	equivalent := &Meta{Name: "s", Description: "d", Location: "/l", RootDir: "/r", Resources: []string{"./references/a.md"}}
	if digestSkillMetas([]*Meta{base}) == digestSkillMetas([]*Meta{changed}) {
		t.Fatal("changing resources should change the digest")
	}
	if digestSkillMetas([]*Meta{base}) != digestSkillMetas([]*Meta{equivalent}) {
		t.Fatal("equivalent resource spellings should share a digest")
	}
}

func writeResourceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestCheckDeclaredResources(t *testing.T) {
	root := t.TempDir()
	writeResourceFile(t, filepath.Join(root, "references", "present.md"), "content")
	writeResourceFile(t, filepath.Join(root, "references", "empty.md"), "")
	if err := os.MkdirAll(filepath.Join(root, "references", "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	outside := t.TempDir()
	writeResourceFile(t, filepath.Join(outside, "secret.md"), "secret")
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "references", "cross.md")); err != nil {
		t.Fatalf("symlink cross-root: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "references", "present.md"), filepath.Join(root, "references", "alias.md")); err != nil {
		t.Fatalf("symlink in-root: %v", err)
	}

	entries := CheckDeclaredResources(root, []string{
		"references/present.md",
		"references/alias.md",
		"references/missing.md",
		"references/empty.md",
		"../escape.md",
		"/absolute.md",
		"references/subdir",
		"references/cross.md",
	})
	if len(entries) != 8 {
		t.Fatalf("entries = %d, want 8", len(entries))
	}
	wantStatus := map[string]string{
		"references/present.md": ResourceStatusPassed,
		"references/alias.md":   ResourceStatusPassed,
		"references/missing.md": ResourceStatusFailed,
		"references/empty.md":   ResourceStatusWarning,
		"../escape.md":          ResourceStatusFailed,
		"/absolute.md":          ResourceStatusFailed,
		"references/subdir":     ResourceStatusFailed,
		"references/cross.md":   ResourceStatusFailed,
	}
	for _, entry := range entries {
		want, ok := wantStatus[entry.Declared]
		if !ok {
			t.Fatalf("unexpected entry %q", entry.Declared)
		}
		if entry.Status != want {
			t.Errorf("%s: status = %q, want %q (detail %q)", entry.Declared, entry.Status, want, entry.Detail)
		}
	}
	if got := SummarizeResourceStatus(entries); got != ResourceStatusFailed {
		t.Fatalf("summary = %q, want failed", got)
	}
	if got := SummarizeResourceStatus(nil); got != "none" {
		t.Fatalf("empty summary = %q, want none", got)
	}
}

func TestCheckDeclaredResources_SymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	writeResourceFile(t, filepath.Join(real, "references", "present.md"), "content")
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "linked-skill")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink root: %v", err)
	}
	entries := CheckDeclaredResources(link, []string{"references/present.md"})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Status != ResourceStatusPassed {
		t.Fatalf("symlinked root should resolve inside itself: %+v", entries[0])
	}
}

func TestPlaceholderResourceRefs(t *testing.T) {
	content := "Run `${CHORD_SKILL_DIR}/scripts/check.sh` and `${CHORD_SKILL_DIR}/references/a.md`, " +
		"again `${CHORD_SKILL_DIR}/references/a.md`. Ignore references/bare.md prose."
	refs := PlaceholderResourceRefs(content)
	if len(refs) != 2 || refs[0] != "scripts/check.sh" || refs[1] != "references/a.md" {
		t.Fatalf("refs = %q, want deduplicated placeholder paths", refs)
	}
	if refs := PlaceholderResourceRefs("no placeholders here"); len(refs) != 0 {
		t.Fatalf("refs = %q, want none", refs)
	}
}

func TestCheckPlaceholderResources_OnlyWarns(t *testing.T) {
	root := t.TempDir()
	writeResourceFile(t, filepath.Join(root, "scripts", "check.sh"), "content")
	content := "Run `${CHORD_SKILL_DIR}/scripts/check.sh` and `${CHORD_SKILL_DIR}/references/missing.md`."
	entries := CheckPlaceholderResources(root, content)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want only the missing placeholder", entries)
	}
	if entries[0].Status != ResourceStatusWarning || !entries[0].Placeholder {
		t.Fatalf("placeholder problems should warn, got %+v", entries[0])
	}
	if entries := CheckPlaceholderResources(root, "Run `${CHORD_SKILL_DIR}/scripts/check.sh`."); len(entries) != 0 {
		t.Fatalf("clean placeholders should produce no entries, got %+v", entries)
	}
}

func TestScanMetaDiagnostic_UnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not restrict root")
	}
	dir := t.TempDir()
	createSkillFile(t, dir, "valid-skill", "Valid skill", "Body\n")
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(locked, "SKILL.md"), []byte("---\nname: locked\ndescription: Locked\n---\nBody\n"), 0o644); err != nil {
		t.Fatalf("write locked skill: %v", err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(locked, 0o755) }()

	loader := NewLoader([]string{dir})
	items, problems := loader.ScanMetaDiagnostic()
	if len(problems) != 1 || problems[0].Path != locked {
		t.Fatalf("problems = %v, want one entry for %s", problems, locked)
	}
	// The readable skill is still reported alongside the problem.
	found := false
	for _, item := range items {
		if item.Name == "valid-skill" {
			found = true
		}
	}
	if !found {
		t.Fatalf("readable skills should still be reported: %+v", items)
	}
}

func TestScanMetaDiagnostic_UnreadableDirBehindSymlink(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not restrict root")
	}
	real := t.TempDir()
	createSkillFile(t, real, "linked-skill", "Linked skill", "Body\n")
	locked := filepath.Join(real, "references")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(locked, 0o755) }()

	dir := t.TempDir()
	link := filepath.Join(dir, "via-symlink")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// The glob follows symlinked directories, so the audit must too: the
	// unreadable directory behind the link has to be reported, not skipped.
	items, problems := NewLoader([]string{dir}).ScanMetaDiagnostic()
	if len(problems) != 1 || problems[0].Path != filepath.Join(link, "references") {
		t.Fatalf("problems = %v, want the unreadable directory under %s", problems, link)
	}
	if len(items) != 1 || items[0].Name != "linked-skill" {
		t.Fatalf("symlinked skill tree should still be discovered: %+v", items)
	}
}

func TestScanMetaDiagnostic_SymlinkCycle(t *testing.T) {
	dir := t.TempDir()
	createSkillFile(t, dir, "loop-skill", "Loop skill", "Body\n")
	if err := os.Symlink(filepath.Join(dir, "loop-skill"), filepath.Join(dir, "loop-skill", "self")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// The runtime glob descends the same cycle a bounded number of times, so the
	// scan mirrors it with shadowed duplicate rows. What matters here is that
	// the audit itself terminates and does not treat the cycle as a problem.
	items, problems := NewLoader([]string{dir}).ScanMetaDiagnostic()
	if len(problems) != 0 {
		t.Fatalf("a symlink cycle is not a scan problem: %v", problems)
	}
	winners := 0
	for _, item := range items {
		if item.Name != "loop-skill" {
			t.Fatalf("unexpected row: %+v", item)
		}
		if !item.Shadowed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("items = %d with %d unshadowed rows, want exactly one winner", len(items), winners)
	}
}

func TestScanMetaDiagnostic_DanglingSymlinkEntry(t *testing.T) {
	dir := t.TempDir()
	createSkillFile(t, dir, "good-skill", "Good skill", "Body\n")
	broken := filepath.Join(dir, "broken-skill")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), broken); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	items, problems := NewLoader([]string{dir}).ScanMetaDiagnostic()
	if len(problems) != 1 || problems[0].Path != broken {
		t.Fatalf("problems = %v, want the dangling symlink %s", problems, broken)
	}
	if len(items) != 1 || items[0].Name != "good-skill" {
		t.Fatalf("items = %+v, want the one valid skill", items)
	}
}

func TestScanMetaDiagnostic_ScanRootProblems(t *testing.T) {
	dir := t.TempDir()
	fileRoot := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "missing"), dangling); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// A missing root is simply unconfigured; a root that exists but is not a
	// directory, or a link with no target, is traversal the glob drops.
	items, problems := NewLoader([]string{fileRoot, dangling, filepath.Join(dir, "missing")}).ScanMetaDiagnostic()
	if len(items) != 0 {
		t.Fatalf("no skill tree should be scanned: %+v", items)
	}
	if len(problems) != 2 {
		t.Fatalf("problems = %v, want one per broken root", problems)
	}
	if problems[0].Path != fileRoot || problems[1].Path != dangling {
		t.Fatalf("problems = %v, want %s and %s", problems, fileRoot, dangling)
	}
	if !strings.Contains(problems[1].String(), "dangling symlink") {
		t.Fatalf("dangling root problem = %v", problems[1])
	}
}

func TestVisibleForRuleset(t *testing.T) {
	metas := []*Meta{{Name: "open-skill"}, {Name: "closed-skill"}, nil}
	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "skill", Pattern: "closed-skill", Action: permission.ActionDeny},
	}
	visible := VisibleForRuleset(metas, ruleset)
	if len(visible) != 1 || visible[0].Name != "open-skill" {
		t.Fatalf("visible = %+v, want only open-skill", visible)
	}
	if !visible[0].Discovered {
		t.Fatal("visible skills should be marked discovered")
	}
	all := VisibleForRuleset(metas, nil)
	if len(all) != 2 {
		t.Fatalf("empty ruleset should keep all skills, got %+v", all)
	}
	ordered := VisibleForRuleset([]*Meta{{Name: "b"}, {Name: "a"}}, nil)
	names := []string{ordered[0].Name, ordered[1].Name}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("names = %q, want sorted", names)
	}
	if !strings.Contains(strings.Join(names, ","), "a") {
		t.Fatal("sanity check")
	}
}
