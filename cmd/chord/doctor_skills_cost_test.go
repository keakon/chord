package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/testgit"
	"github.com/keakon/chord/internal/tools"
)

// runDoctorSkillsJSONWithAgent runs the doctor under a chosen role and
// returns the report even when the command fails, so a test can assert on
// both the report and the exit code.
func runDoctorSkillsJSONWithAgent(t *testing.T, agent string) (doctorSkillsReport, error) {
	t.Helper()
	var out bytes.Buffer
	err := runDoctorSkills(doctorSkillsOptions{Out: &out, JSON: true, Agent: agent})
	var report doctorSkillsReport
	if out.Len() == 0 {
		// The command failed before it could produce a report.
		return report, err
	}
	if decodeErr := json.Unmarshal(out.Bytes(), &report); decodeErr != nil {
		t.Fatalf("decode JSON report: %v\n%s", decodeErr, out.String())
	}
	return report, err
}

// writeDoctorAgentConfig writes a role definition into the project's agent
// directory.
func writeDoctorAgentConfig(t *testing.T, projectRoot, name, body string) {
	t.Helper()
	agentsDir := filepath.Join(projectRoot, ".chord", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", agentsDir, err)
	}
	path := filepath.Join(agentsDir, name+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// resolveDoctorTestPath resolves symlinks so a path reported by git can be
// compared with a temporary directory.
func resolveDoctorTestPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolved
}

func runDoctorGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

func TestRunDoctorSkills_AgentFlagSelectsTheConfiguredBaseline(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"), "closed-skill", "Closed skill", "Body\n")
	writeDoctorAgentConfig(t, projectRoot, "reviewer", "name: reviewer\npermission:\n  skill:\n    closed-skill: deny\n")

	// The default baseline is the built-in builder, which allows everything.
	report := runDoctorSkillsJSON(t, false)
	if report.Context.Agent != "builder" || report.Context.AgentSource != "builtin" {
		t.Fatalf("context = %+v, want the built-in builder", report.Context)
	}
	entry := findDoctorSkillEntry(report, "closed-skill")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if entry.Visibility != "visible" || entry.Invocation != "allow" {
		t.Fatalf("builder baseline = %+v, want a visible and allowed skill", entry)
	}

	// The selected role's own rules decide the verdict.
	roleReport, err := runDoctorSkillsJSONWithAgent(t, "reviewer")
	if err != nil {
		t.Fatalf("runDoctorSkills: %v", err)
	}
	if roleReport.Context.Agent != "reviewer" || roleReport.Context.AgentSource != "project" {
		t.Fatalf("context = %+v, want the project-defined reviewer", roleReport.Context)
	}
	roleEntry := findDoctorSkillEntry(roleReport, "closed-skill")
	if roleEntry == nil {
		t.Fatalf("missing entry: %+v", roleReport.Entries)
	}
	if roleEntry.Visibility != "denied" || roleEntry.Invocation != "deny" {
		t.Fatalf("reviewer baseline = %+v, want a denied skill", roleEntry)
	}
	if roleEntry.Startup != "not_listed" || roleEntry.StartupReason != "denied_by_baseline" {
		t.Fatalf("reviewer startup = %+v, want the skill excluded from the listing", roleEntry)
	}
}

func TestRunDoctorSkills_UnknownAgentFailsExplicitly(t *testing.T) {
	setupDoctorSkillsProject(t)

	report, err := runDoctorSkillsJSONWithAgent(t, "ghost")
	exitErr, ok := errors.AsType[cliExitError](err)
	if !ok || exitErr.code != 2 {
		t.Fatalf("err = %v, want exit 2 for an unknown role", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error should name the role: %v", err)
	}
	if len(report.Entries) != 0 {
		t.Fatalf("an unknown role must not produce rows: %+v", report.Entries)
	}
}

func TestRunDoctorSkills_BuiltinRolesNeedNoConfigFile(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"), "any", "Any skill", "Body\n")

	for _, role := range []string{"builder", "planner"} {
		report, err := runDoctorSkillsJSONWithAgent(t, role)
		if err != nil {
			t.Fatalf("role %q: %v", role, err)
		}
		if report.Context.AgentSource != "builtin" {
			t.Fatalf("role %q source = %q, want builtin", role, report.Context.AgentSource)
		}
		if report.Context.AgentError != "" {
			t.Fatalf("role %q must not report a baseline error: %s", role, report.Context.AgentError)
		}
		entry := findDoctorSkillEntry(report, "any")
		if entry == nil || entry.Visibility != "visible" {
			t.Fatalf("role %q entry = %+v, want a visible skill", role, entry)
		}
	}
}

func TestRunDoctorSkills_WorkerRoleReportsItsOwnBaselineOnly(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"), "any", "Any skill", "Body\n")
	writeDoctorAgentConfig(t, projectRoot, "worker", "name: worker\nmode: subagent\npermission:\n  \"*\": allow\n")

	report, err := runDoctorSkillsJSONWithAgent(t, "worker")
	if err != nil {
		t.Fatalf("runDoctorSkills: %v", err)
	}
	if report.Context.AgentScope != "worker" {
		t.Fatalf("scope = %q, want worker", report.Context.AgentScope)
	}
	if !strings.Contains(report.Context.AgentNote, "parent") {
		t.Fatalf("note = %q, want it to state that the parent rules are not included", report.Context.AgentNote)
	}
	entry := findDoctorSkillEntry(report, "any")
	if entry == nil || entry.Visibility != "visible" {
		t.Fatalf("entry = %+v, want a visible skill", entry)
	}
}

func TestRunDoctorSkills_ExternalBackendRoleIsNotApplicable(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"), "any", "Any skill", "Body\n")
	writeDoctorAgentConfig(t, projectRoot, "ext", "name: ext\nmode: subagent\nbackend:\n  kind: claude-code\n  command: claude\n")

	report, err := runDoctorSkillsJSONWithAgent(t, "ext")
	if err != nil {
		t.Fatalf("runDoctorSkills: %v", err)
	}
	if report.Context.AgentScope != "external_backend" {
		t.Fatalf("scope = %q, want external_backend", report.Context.AgentScope)
	}
	entry := findDoctorSkillEntry(report, "any")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if entry.Visibility != "not_applicable" {
		t.Fatalf("visibility = %q, want not_applicable", entry.Visibility)
	}
	if entry.Invocation != "unknown" {
		t.Fatalf("invocation = %q, want unknown", entry.Invocation)
	}
	if report.Startup.Available {
		t.Fatal("an external backend role has no local startup listing")
	}
	if report.Startup.Reason != "not_applicable" {
		t.Fatalf("startup reason = %q, want not_applicable", report.Startup.Reason)
	}
	// An external backend never evaluates Chord's permission rules, so the
	// "no rules means every call denies" hint would misdescribe it.
	if strings.Contains(report.Context.AgentNote, "declares no permission rules") {
		t.Fatalf("agent note = %q, want no permission-rule hint for an external backend role", report.Context.AgentNote)
	}
}

func TestRunDoctorSkills_StartupListingDropsEntriesPastTheCap(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	for i := range tools.SkillListingMaxEntries + 2 {
		writeDoctorSkill(t, skillDir, fmt.Sprintf("skill-%02d", i), "Short description", "Body\n")
	}

	report := runDoctorSkillsJSON(t, false)
	if report.Startup.Shown != tools.SkillListingMaxEntries {
		t.Fatalf("shown = %d, want %d", report.Startup.Shown, tools.SkillListingMaxEntries)
	}
	if report.Startup.NameOnly != 2 {
		t.Fatalf("name only = %d, want 2", report.Startup.NameOnly)
	}
	dropped := findDoctorSkillEntry(report, "skill-33")
	if dropped == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if dropped.Startup != "name_only" {
		t.Fatalf("startup = %q, want name_only", dropped.Startup)
	}
	if dropped.StartupReason != tools.SkillListingOmitEntryLimit {
		t.Fatalf("startup reason = %q, want %q", dropped.StartupReason, tools.SkillListingOmitEntryLimit)
	}
	listed := findDoctorSkillEntry(report, "skill-00")
	if listed == nil || listed.Startup != "listed" {
		t.Fatalf("first entry = %+v, want a described listing", listed)
	}
}

func TestRunDoctorSkills_TruncatesAnOverlongDescription(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	longDesc := strings.Repeat("厂", tools.SkillListingMaxDescCharsPerEntry+500)
	writeDoctorSkill(t, skillDir, "long-desc", longDesc, "Body\n")

	report := runDoctorSkillsJSON(t, false)
	entry := findDoctorSkillEntry(report, "long-desc")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if entry.Startup != "listed_truncated" {
		t.Fatalf("startup = %q, want listed_truncated", entry.Startup)
	}
	if entry.Description.Truncated == nil || !*entry.Description.Truncated {
		t.Fatal("the description should be reported as truncated")
	}
	if entry.Description.RawChars != tools.SkillListingMaxDescCharsPerEntry+500 {
		t.Fatalf("raw chars = %d", entry.Description.RawChars)
	}
	if entry.Description.RawBytes != (tools.SkillListingMaxDescCharsPerEntry+500)*3 {
		t.Fatalf("raw bytes = %d, want the UTF-8 length", entry.Description.RawBytes)
	}
	if entry.Description.RenderedChars == nil || *entry.Description.RenderedChars != tools.SkillListingMaxDescCharsPerEntry+2 {
		t.Fatalf("rendered chars = %v, want %d including JSON quotes", entry.Description.RenderedChars, tools.SkillListingMaxDescCharsPerEntry+2)
	}
}

func TestRunDoctorSkills_SizesSeparateSourceBodyAndRender(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	sizedDir := filepath.Join(skillDir, "sized")
	if err := os.MkdirAll(sizedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nname: sized\ndescription: Sized skill\n---\nBody line\r\n"
	if err := os.WriteFile(filepath.Join(sizedDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	report := runDoctorSkillsJSON(t, false)
	entry := findDoctorSkillEntry(report, "sized")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if entry.Sizes.SourceBytes != len(content) {
		t.Fatalf("source bytes = %d, want %d", entry.Sizes.SourceBytes, len(content))
	}
	if entry.Sizes.BodyBytes != len("Body line\n") {
		t.Fatalf("body bytes = %d, want the CRLF-normalized body", entry.Sizes.BodyBytes)
	}
	if entry.Sizes.BodyChars != len("Body line\n") {
		t.Fatalf("body chars = %d", entry.Sizes.BodyChars)
	}
	if entry.Sizes.RenderBytes <= entry.Sizes.BodyBytes {
		t.Fatalf("render bytes = %d, want more than the body", entry.Sizes.RenderBytes)
	}
	if entry.Sizes.EnvelopeBytes <= 0 {
		t.Fatalf("envelope bytes = %d, want the wrapper measured on its own", entry.Sizes.EnvelopeBytes)
	}
	if entry.Sizes.OverInlineBudget {
		t.Fatal("a small skill must not be reported as over the inline budget")
	}
}

func TestRunDoctorSkills_LargeSkillIsAnActionNotAFailure(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	writeDoctorSkill(t, skillDir, "big", "Big skill", strings.Repeat("x", 60*1024))

	report, err := runDoctorSkillsJSONAllowFailure(t, false)
	if err != nil {
		t.Fatalf("a large skill must not fail the command: %v", err)
	}
	entry := findDoctorSkillEntry(report, "big")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if !entry.Sizes.OverInlineBudget {
		t.Fatal("a body over the inline budget must be reported")
	}
	found := false
	for _, action := range report.Actions {
		if action.Kind == doctorSkillActionOverInlineBudget && action.Skill == "big" {
			found = true
		}
	}
	if !found {
		t.Fatalf("actions = %+v, want an over_inline_budget item", report.Actions)
	}
}

func TestRunDoctorSkills_ShadowedLargeSkillKeepsSizesWithoutRuntimeCostAction(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"), "shared", "Active skill", "Body\n")
	writeDoctorSkill(t, filepath.Join(projectRoot, ".agents", "skills"), "shared", "Shadowed skill", strings.Repeat("x", 60*1024))

	report := runDoctorSkillsJSON(t, false)
	foundShadowed := false
	for _, entry := range report.Entries {
		if entry.Name == "shared" && entry.Shadowed {
			foundShadowed = true
			if !entry.Sizes.OverInlineBudget || entry.Sizes.BodyBytes != 60*1024 {
				t.Fatalf("shadowed size metrics = %+v, want the full body measured", entry.Sizes)
			}
		}
	}
	if !foundShadowed {
		t.Fatalf("missing shadowed entry: %+v", report.Entries)
	}
	foundShadowAction := false
	for _, action := range report.Actions {
		if action.Kind == doctorSkillActionOverInlineBudget {
			t.Fatalf("shadowed skill produced a runtime cost action: %+v", action)
		}
		if action.Kind == doctorSkillActionShadowed && action.Skill == "shared" {
			foundShadowAction = true
		}
	}
	if !foundShadowAction {
		t.Fatalf("missing shadowing action: %+v", report.Actions)
	}
}

func TestRunDoctorSkills_ReportsTheSidecarSource(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	sidecarDir := filepath.Join(skillDir, "sidecar-skill")
	if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sidecarDir, "SKILL.md"), []byte("---\nname: sidecar-skill\ndescription: Sidecar skill\n---\nBody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sidecarDir, "chord.yaml"), []byte("when_to_use: \"from the sidecar\"\n"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	report := runDoctorSkillsJSON(t, false)
	entry := findDoctorSkillEntry(report, "sidecar-skill")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if entry.Sidecar != "chord.yaml" {
		t.Fatalf("sidecar = %q, want chord.yaml", entry.Sidecar)
	}
}

func TestRunDoctorSkills_ReportsConditionalAndInvalidPaths(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	conditionalDir := filepath.Join(skillDir, "conditional")
	if err := os.MkdirAll(conditionalDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	conditional := "---\nname: conditional\ndescription: Conditional skill\npaths:\n  - \"cmd/agent/**\"\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(conditionalDir, "SKILL.md"), []byte(conditional), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	invalidDir := filepath.Join(skillDir, "invalid-paths")
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	invalid := "---\nname: invalid-paths\ndescription: Invalid paths skill\npaths:\n  - \"/absolute/**\"\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(invalidDir, "SKILL.md"), []byte(invalid), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	report := runDoctorSkillsJSON(t, false)
	conditionalEntry := findDoctorSkillEntry(report, "conditional")
	if conditionalEntry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if conditionalEntry.PathsStatus != "conditional" {
		t.Fatalf("paths status = %q, want conditional", conditionalEntry.PathsStatus)
	}
	if conditionalEntry.Startup != "not_listed" || conditionalEntry.StartupReason != "paths_conditional" {
		t.Fatalf("startup = %+v, want the skill waiting for a matching path", conditionalEntry)
	}
	if conditionalEntry.Visibility != "visible" {
		t.Fatalf("visibility = %q, want visible: a conditional skill stays loadable by name", conditionalEntry.Visibility)
	}

	invalidEntry := findDoctorSkillEntry(report, "invalid-paths")
	if invalidEntry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if invalidEntry.PathsStatus != "invalid" {
		t.Fatalf("paths status = %q, want invalid", invalidEntry.PathsStatus)
	}
	if invalidEntry.StartupReason != "paths_invalid" {
		t.Fatalf("startup reason = %q, want paths_invalid", invalidEntry.StartupReason)
	}
	if invalidEntry.Reason != doctorSkillReasonInvalidPaths {
		t.Fatalf("reason = %q, want %q", invalidEntry.Reason, doctorSkillReasonInvalidPaths)
	}
	if invalidEntry.Visibility != "visible" {
		t.Fatalf("visibility = %q, want visible: an invalid pattern does not cancel name-based loading", invalidEntry.Visibility)
	}
}

func TestRunDoctorSkills_ContentRootFollowsTheLinkedWorktree(t *testing.T) {
	mainRoot := testgit.NewRepository(t, map[string]string{"README.md": "fixture\n"})
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	runDoctorGit(t, mainRoot, "worktree", "add", "-q", "-b", "feature", linkedRoot)

	// The skill lives in the main worktree only, so the doctor has to resolve
	// the content root to find it.
	writeDoctorSkill(t, filepath.Join(mainRoot, ".chord", "skills"), "shared-skill", "Shared skill", "Body\n")
	setupDoctorConfigHome(t, "providers:\n  sample:\n    type: responses\n    models:\n      test-model:\n        limit:\n          context: 100000\n          output: 64000\n")
	t.Chdir(linkedRoot)

	report := runDoctorSkillsJSON(t, false)
	wantContentRoot := resolveDoctorTestPath(t, mainRoot)
	if resolveDoctorTestPath(t, report.Context.ContentRoot) != wantContentRoot {
		t.Fatalf("content root = %q, want the main worktree %q", report.Context.ContentRoot, wantContentRoot)
	}
	if report.Context.CheckoutRoot == "" {
		t.Fatal("the checkout root should be reported next to the content root")
	}
	entry := findDoctorSkillEntry(report, "shared-skill")
	if entry == nil {
		t.Fatalf("the content-root skill was not found: %+v", report.Entries)
	}
	if entry.Source != "project" || entry.Scope != "content_root_fallback" {
		t.Fatalf("entry = %+v, want the content-root fallback directory", entry)
	}
}

func TestRunDoctorSkills_ReportsAnAskAction(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"), "guarded", "Guarded skill", "Body\n")
	writeDoctorAgentConfig(t, projectRoot, "reviewer", "name: reviewer\npermission:\n  skill:\n    guarded: ask\n")

	report, err := runDoctorSkillsJSONWithAgent(t, "reviewer")
	if err != nil {
		t.Fatalf("runDoctorSkills: %v", err)
	}
	entry := findDoctorSkillEntry(report, "guarded")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	// Visibility only covers the ruleset: an ask still leaves the skill in the
	// catalog, so the two dimensions have to disagree here.
	if entry.Visibility != "visible" {
		t.Fatalf("visibility = %q, want visible", entry.Visibility)
	}
	if entry.Invocation != "ask" {
		t.Fatalf("invocation = %q, want ask", entry.Invocation)
	}
}

func TestRunDoctorSkills_ReportsAManualOnlySkill(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	manualDir := filepath.Join(skillDir, "manual")
	if err := os.MkdirAll(manualDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manual := "---\nname: manual\ndescription: Manual only skill\ndisable-model-invocation: true\n---\nBody\n"
	if err := os.WriteFile(filepath.Join(manualDir, "SKILL.md"), []byte(manual), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	report := runDoctorSkillsJSON(t, false)
	entry := findDoctorSkillEntry(report, "manual")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if entry.ModelInvocation != "manual_only" {
		t.Fatalf("model invocation = %q, want manual_only", entry.ModelInvocation)
	}
	// The ruleset still allows the name, so visibility is unaffected.
	if entry.Visibility != "visible" {
		t.Fatalf("visibility = %q, want visible", entry.Visibility)
	}
	if entry.Startup != "not_listed" || entry.StartupReason != "model_invocation_disabled" {
		t.Fatalf("startup = %+v, want the skill kept out of the listing", entry)
	}
}

func TestRunDoctorSkills_ReportsTheResourceWarningSize(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	writeDoctorSkillWithResources(t, skillDir, "res-skill", "Resource skill", "Body\n", []string{"references/missing.md"})

	report := runDoctorSkillsJSON(t, false)
	entry := findDoctorSkillEntry(report, "res-skill")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if entry.Resources != "failed" {
		t.Fatalf("resources = %q, want failed", entry.Resources)
	}
	if entry.Sizes.WarningBytes <= 0 {
		t.Fatalf("warning bytes = %d, want the resource warning measured on its own", entry.Sizes.WarningBytes)
	}
	if entry.Sizes.EnvelopeBytes <= entry.Sizes.WarningBytes {
		t.Fatalf("envelope bytes = %d, want more than the warning block", entry.Sizes.EnvelopeBytes)
	}
}

func TestRunDoctorSkills_RowOrderDoesNotChangeTheListedEntries(t *testing.T) {
	for _, tc := range []struct {
		name, description, reason string
		count                     int
	}{
		{"entry limit", "Sample skill", tools.SkillListingOmitEntryLimit, tools.SkillListingMaxEntries},
		{"byte budget", strings.Repeat("D", tools.SkillListingMaxDescCharsPerEntry), tools.SkillListingOmitByteBudget, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, _ := setupDoctorSkillsProject(t)
			for i := range tc.count {
				writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"),
					fmt.Sprintf("project-%02d", i), tc.description, "Body\n")
			}
			// This path sorts first in the report, but is last in the catalog.
			writeDoctorSkill(t, filepath.Join(projectRoot, ".agents", "skills"), "from-agents", tc.description, "Body\n")
			report := runDoctorSkillsJSON(t, false)
			entry := findDoctorSkillEntry(report, "from-agents")
			if entry == nil || entry.Startup != doctorSkillStartupNameOnly || entry.StartupReason != tc.reason {
				t.Fatalf("last catalog entry = %+v, want name-only due to %s", entry, tc.reason)
			}
			if report.Entries[0].Name != "from-agents" {
				t.Fatalf("first report row = %s", report.Entries[0].Name)
			}
			first := findDoctorSkillEntry(report, "project-00")
			if first == nil || first.Startup != doctorSkillStartupListed {
				t.Fatalf("first catalog entry = %+v, want listed", first)
			}
		})
	}
}

func TestRunDoctorSkills_UsesTheContentRootProjectConfig(t *testing.T) {
	mainRoot := testgit.NewRepository(t, map[string]string{"README.md": "fixture\n"})
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	runDoctorGit(t, mainRoot, "worktree", "add", "-q", "-b", "feature", linkedRoot)

	// The project configuration and the role definitions live in the content
	// root; the checkout carries a different project configuration that must
	// not be used.
	mainExtra := filepath.Join(mainRoot, "main-extra-skills")
	writeDoctorSkill(t, mainExtra, "from-content-root", "Content root skill", "Body\n")
	if err := os.MkdirAll(filepath.Join(mainRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	projectConfig := "skills:\n  paths:\n    - " + mainExtra + "\n"
	if err := os.WriteFile(filepath.Join(mainRoot, ".chord", "config.yaml"), []byte(projectConfig), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	writeDoctorAgentConfig(t, mainRoot, "reviewer", "name: reviewer\npermission:\n  \"*\": allow\n")

	linkedExtra := filepath.Join(linkedRoot, "linked-extra-skills")
	writeDoctorSkill(t, linkedExtra, "from-checkout", "Checkout skill", "Body\n")
	if err := os.MkdirAll(filepath.Join(linkedRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	linkedConfig := "skills:\n  paths:\n    - " + linkedExtra + "\n"
	if err := os.WriteFile(filepath.Join(linkedRoot, ".chord", "config.yaml"), []byte(linkedConfig), 0o644); err != nil {
		t.Fatalf("write checkout config: %v", err)
	}

	setupDoctorConfigHome(t, "providers:\n  sample:\n    type: responses\n    models:\n      test-model:\n        limit:\n          context: 100000\n          output: 64000\n")
	t.Chdir(linkedRoot)

	report, err := runDoctorSkillsJSONWithAgent(t, "reviewer")
	if err != nil {
		t.Fatalf("runDoctorSkills: %v", err)
	}
	if report.Context.AgentSource != "project" {
		t.Fatalf("agent source = %q, want the content root definition", report.Context.AgentSource)
	}
	if entry := findDoctorSkillEntry(report, "from-content-root"); entry == nil {
		t.Fatalf("the content root's extra skill path was not used: %+v", report.Entries)
	}
	if entry := findDoctorSkillEntry(report, "from-checkout"); entry != nil {
		t.Fatalf("the checkout's project configuration must not be used: %+v", report.Entries)
	}
}

func TestResolveSkillDirs_OrdersTheRuntimeSearchPath(t *testing.T) {
	contentRoot := t.TempDir()
	checkout := t.TempDir()
	// A linked worktree root carries a .git file rather than a directory.
	if err := os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(checkout, "pkg", "service")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configHome := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra")

	dirs := ResolveSkillDirs(SkillDirsInput{
		ContentRoot: contentRoot,
		WorkDir:     workDir,
		ConfigHome:  configHome,
		Extra:       []string{extra, "  "},
	})
	want := []string{
		filepath.Join(checkout, ".chord", "skills"),
		filepath.Join(checkout, ".agents", "skills"),
		filepath.Join(workDir, ".agents", "skills"),
		filepath.Join(checkout, "pkg", ".agents", "skills"),
		filepath.Join(contentRoot, ".chord", "skills"),
		filepath.Join(contentRoot, ".agents", "skills"),
		filepath.Join(configHome, "skills"),
		extra,
	}
	got := SkillDirPaths(dirs)
	if len(got) != len(want) {
		t.Fatalf("dirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dirs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if dirs[0].Scope != skillDirScopeCheckout || dirs[2].Scope != skillDirScopeWorkdirChain {
		t.Fatalf("scopes = %+v", dirs)
	}
	if dirs[4].Scope != skillDirScopeContentRoot || dirs[6].Scope != skillDirScopeGlobal || dirs[7].Scope != skillDirScopeExtra {
		t.Fatalf("scopes = %+v", dirs)
	}
}
