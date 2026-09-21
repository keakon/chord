package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/skill"
)

func writeDoctorSkill(t *testing.T, parentDir, name, description, body string) string {
	t.Helper()
	skillDir := filepath.Join(parentDir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	var sb strings.Builder
	if name != "" || description != "" {
		sb.WriteString("---\n")
		if name != "" {
			sb.WriteString("name: ")
			sb.WriteString(strconv.Quote(name))
			sb.WriteString("\n")
		}
		if description != "" {
			sb.WriteString("description: ")
			sb.WriteString(strconv.Quote(description))
			sb.WriteString("\n")
		}
		sb.WriteString("---\n")
	}
	sb.WriteString(body)
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", skillPath, err)
	}
	return skillPath
}

func writeDoctorSkillWithResources(t *testing.T, parentDir, name, description, body string, resources []string) string {
	t.Helper()
	skillDir := filepath.Join(parentDir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	if name != "" {
		sb.WriteString("name: ")
		sb.WriteString(strconv.Quote(name))
		sb.WriteString("\n")
	}
	if description != "" {
		sb.WriteString("description: ")
		sb.WriteString(strconv.Quote(description))
		sb.WriteString("\n")
	}
	if len(resources) > 0 {
		sb.WriteString("resources:\n")
		for _, resource := range resources {
			sb.WriteString("  - ")
			sb.WriteString(resource)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("---\n")
	sb.WriteString(body)
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", skillPath, err)
	}
	return skillPath
}

func setupDoctorSkillsProject(t *testing.T) (projectRoot, configHome string) {
	t.Helper()
	setupDoctorConfigHome(t, "providers:\n  sample:\n    type: responses\n    models:\n      test-model:\n        limit:\n          context: 100000\n          output: 64000\n")
	configHome = os.Getenv("CHORD_CONFIG_HOME")
	projectRoot = t.TempDir()
	t.Chdir(projectRoot)
	return projectRoot, configHome
}

func runDoctorSkillsJSON(t *testing.T, strict bool) doctorSkillsReport {
	t.Helper()
	report, err := runDoctorSkillsJSONAllowFailure(t, strict)
	if err != nil {
		t.Fatalf("runDoctorSkills: %v", err)
	}
	return report
}

func runDoctorSkillsJSONAllowFailure(t *testing.T, strict bool) (doctorSkillsReport, error) {
	t.Helper()
	var out bytes.Buffer
	err := runDoctorSkills(doctorSkillsOptions{Out: &out, JSON: true, Strict: strict})
	var report doctorSkillsReport
	if decodeErr := json.Unmarshal(out.Bytes(), &report); decodeErr != nil {
		t.Fatalf("decode JSON report: %v\n%s", decodeErr, out.String())
	}
	if err != nil {
		if exitErr, ok := errors.AsType[cliExitError](err); ok && exitErr.code == 1 {
			return report, err
		}
		t.Fatalf("runDoctorSkills: %v", err)
	}
	return report, nil
}

func findDoctorSkillEntry(report doctorSkillsReport, name string) *doctorSkillEntry {
	for i := range report.Entries {
		if report.Entries[i].Name == name {
			return &report.Entries[i]
		}
	}
	return nil
}

func TestRunDoctorSkills_Healthy(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	writeDoctorSkillWithResources(t, skillDir, "healthy", "Healthy skill", "Body\n", []string{"references/present.md"})
	if err := os.MkdirAll(filepath.Join(skillDir, "healthy", "references"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "healthy", "references", "present.md"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write resource: %v", err)
	}

	report := runDoctorSkillsJSON(t, false)
	if report.Ruleset != "builder" {
		t.Fatalf("ruleset = %q, want builder", report.Ruleset)
	}
	entry := findDoctorSkillEntry(report, "healthy")
	if entry == nil {
		t.Fatalf("missing healthy entry: %+v", report.Entries)
	}
	if entry.Integrity != "passed" || entry.Load != "passed" || entry.Visibility != "visible" || entry.Resources != "passed" {
		t.Fatalf("entry = %+v, want all passed/visible", entry)
	}
	if entry.Shadowed {
		t.Fatal("healthy skill should not be shadowed")
	}
	if report.Summary.Total != 1 || report.Summary.IntegrityFailed != 0 || report.Summary.LoadFailed != 0 {
		t.Fatalf("summary = %+v, want clean", report.Summary)
	}
}

func TestRunDoctorSkills_InvalidRows(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	writeDoctorSkill(t, skillDir, "good", "Good skill", "Body\n")
	brokenDir := filepath.Join(skillDir, "broken")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "SKILL.md"), []byte("---\n: invalid yaml [\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write broken: %v", err)
	}
	noNameDir := filepath.Join(skillDir, "noname")
	if err := os.MkdirAll(noNameDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(noNameDir, "SKILL.md"), []byte("---\ndescription: no name\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write noname: %v", err)
	}
	noDescDir := filepath.Join(skillDir, "nodesc")
	if err := os.MkdirAll(noDescDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(noDescDir, "SKILL.md"), []byte("---\nname: nodesc\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write nodesc: %v", err)
	}

	var out bytes.Buffer
	err := runDoctorSkills(doctorSkillsOptions{Out: &out, JSON: true})
	exitErr, ok := errors.AsType[cliExitError](err)
	if !ok || exitErr.code != 1 {
		t.Fatalf("err = %v, want exit 1", err)
	}
	var report doctorSkillsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if report.Summary.IntegrityFailed != 3 {
		t.Fatalf("integrity_failed = %d, want 3: %+v", report.Summary.IntegrityFailed, report.Entries)
	}
	for _, entry := range report.Entries {
		if entry.Integrity == "failed" && strings.TrimSpace(entry.Detail) == "" {
			t.Fatalf("failed rows must carry the raw error: %+v", entry)
		}
	}
	good := findDoctorSkillEntry(report, "good")
	if good == nil || good.Integrity != "passed" {
		t.Fatalf("valid skill should stay passed: %+v", report.Entries)
	}
}

func TestRunDoctorSkills_ResourceMissingStaysVisible(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	writeDoctorSkillWithResources(t, skillDir, "res-skill", "Resource skill", "Body\n", []string{"references/missing.md"})

	report := runDoctorSkillsJSON(t, false)
	entry := findDoctorSkillEntry(report, "res-skill")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if entry.Integrity != "passed" || entry.Load != "passed" || entry.Visibility != "visible" {
		t.Fatalf("resource problems must not break load or visibility: %+v", entry)
	}
	if entry.Resources != "failed" {
		t.Fatalf("resources = %q, want failed: %+v", entry.Resources, entry)
	}
	if entry.Reason == "" {
		t.Fatal("resource failures should set a reason")
	}
}

func TestRunDoctorSkills_InvalidDoesNotShadow(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	highDir := filepath.Join(projectRoot, ".chord", "skills")
	lowDir := filepath.Join(projectRoot, ".agents", "skills")
	invalidDir := filepath.Join(highDir, "shared-invalid")
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, "SKILL.md"), []byte("---\nname: shared\ndescription: \n---\nbody\n"), 0o644); err != nil {
		// Fall back to a missing-description file through the helper path.
		t.Fatalf("write invalid: %v", err)
	}
	_ = lowDir
	writeDoctorSkill(t, filepath.Join(projectRoot, ".agents", "skills"), "shared", "Shared skill", "Body\n")

	report, err := runDoctorSkillsJSONAllowFailure(t, false)
	if err == nil {
		t.Fatalf("expected exit 1 for the invalid row, got nil")
	}
	entry := findDoctorSkillEntry(report, "shared")
	if entry == nil {
		t.Fatalf("missing shared entry: %+v", report.Entries)
	}
	if entry.Shadowed {
		t.Fatalf("invalid high-priority file must not shadow the valid skill: %+v", entry)
	}
}

func TestRunDoctorSkills_Shadowing(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"), "shared", "High version", "High\n")
	writeDoctorSkill(t, filepath.Join(projectRoot, ".agents", "skills"), "shared", "Low version", "Low\n")

	report := runDoctorSkillsJSON(t, false)
	if report.Summary.Shadowed != 1 {
		t.Fatalf("shadowed = %d, want 1: %+v", report.Summary.Shadowed, report.Entries)
	}
	shows := 0
	for _, entry := range report.Entries {
		if entry.Name == "shared" && !entry.Shadowed {
			shows++
		}
	}
	if shows != 1 {
		t.Fatalf("expected exactly one unshadowed shared row: %+v", report.Entries)
	}
}

func TestRunDoctorSkills_Denied(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"), "closed-skill", "Closed skill", "Body\n")
	agentsDir := filepath.Join(projectRoot, ".chord", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	denyCfg := "name: builder\npermission:\n  skill:\n    closed-skill: deny\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "builder.yaml"), []byte(denyCfg), 0o644); err != nil {
		t.Fatalf("write builder agent: %v", err)
	}

	report := runDoctorSkillsJSON(t, false)
	entry := findDoctorSkillEntry(report, "closed-skill")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if entry.Visibility != "denied" || entry.Integrity != "passed" || entry.Load != "passed" {
		t.Fatalf("denied skill should stay loadable but hidden: %+v", entry)
	}
	if report.Summary.Denied != 1 {
		t.Fatalf("denied = %d, want 1", report.Summary.Denied)
	}

	// View consistency: the doctor visible set must match the shared helper.
	ruleset, _, rulesetErr := buildDoctorSkillsRuleset(projectRoot, os.Getenv("CHORD_CONFIG_HOME"))
	if rulesetErr != nil {
		t.Fatalf("ruleset: %v", rulesetErr)
	}
	loader := skill.NewLoader(doctorSkillDirs(projectRoot, os.Getenv("CHORD_CONFIG_HOME"), nil))
	metas, err := loader.ScanMeta()
	if err != nil {
		t.Fatalf("ScanMeta: %v", err)
	}
	wantVisible := map[string]bool{}
	for _, meta := range skill.VisibleForRuleset(metas, ruleset) {
		wantVisible[meta.Name] = true
	}
	for _, reportEntry := range report.Entries {
		if reportEntry.Shadowed || reportEntry.Integrity == "failed" {
			continue
		}
		want := wantVisible[reportEntry.Name]
		got := reportEntry.Visibility == "visible"
		if want != got {
			t.Fatalf("skill %q visible = %v, want %v", reportEntry.Name, got, want)
		}
	}
}

func TestRunDoctorSkills_RulesetUnavailable(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"), "any", "Any skill", "Body\n")
	agentsDir := filepath.Join(projectRoot, ".chord", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Invalid agent config forces the ruleset fallback.
	badCfg := "name: builder\ndelegation:\n  max_children: 99999\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "builder.yaml"), []byte(badCfg), 0o644); err != nil {
		t.Fatalf("write bad agent: %v", err)
	}

	report := runDoctorSkillsJSON(t, false)
	entry := findDoctorSkillEntry(report, "any")
	if entry == nil {
		t.Fatalf("missing entry: %+v", report.Entries)
	}
	if entry.Visibility != "not_checked" {
		t.Fatalf("visibility = %q, want not_checked: %+v", entry.Visibility, entry)
	}

	var out bytes.Buffer
	err := runDoctorSkills(doctorSkillsOptions{Out: &out, JSON: true, Strict: true})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 1 {
		t.Fatalf("strict err = %v, want exit 1", err)
	}
}

func TestRunDoctorSkills_UnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not restrict root")
	}
	projectRoot, _ := setupDoctorSkillsProject(t)
	skillDir := filepath.Join(projectRoot, ".chord", "skills")
	writeDoctorSkill(t, skillDir, "good", "Good skill", "Body\n")
	locked := filepath.Join(skillDir, "locked")
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

	var out bytes.Buffer
	err := runDoctorSkills(doctorSkillsOptions{Out: &out, JSON: true})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 {
		t.Fatalf("err = %v, want exit 2 for the unreadable directory", err)
	}
	// The report is still printed, with the unreadable path and the rows that
	// could be read.
	var report doctorSkillsReport
	if decodeErr := json.Unmarshal(out.Bytes(), &report); decodeErr != nil {
		t.Fatalf("decode: %v\n%s", decodeErr, out.String())
	}
	if len(report.ScanIssues) != 1 || !strings.Contains(report.ScanIssues[0], locked) {
		t.Fatalf("scan_issues = %v, want the unreadable %s", report.ScanIssues, locked)
	}
	if entry := findDoctorSkillEntry(report, "good"); entry == nil || entry.Integrity != "passed" {
		t.Fatalf("readable skills should still be reported: %+v", report.Entries)
	}
}

func TestRunDoctorSkills_DanglingRoot(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	if err := os.MkdirAll(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(filepath.Join(projectRoot, "missing-skills"), filepath.Join(projectRoot, ".chord", "skills")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var out bytes.Buffer
	err := runDoctorSkills(doctorSkillsOptions{Out: &out, JSON: true})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 {
		t.Fatalf("err = %v, want exit 2 for the dangling scan root", err)
	}
	var report doctorSkillsReport
	if decodeErr := json.Unmarshal(out.Bytes(), &report); decodeErr != nil {
		t.Fatalf("decode: %v\n%s", decodeErr, out.String())
	}
	if len(report.ScanIssues) != 1 || !strings.Contains(report.ScanIssues[0], "dangling symlink") {
		t.Fatalf("scan_issues = %v, want the dangling scan root", report.ScanIssues)
	}
	if report.Summary.Total != 0 {
		t.Fatalf("total = %d, want no rows", report.Summary.Total)
	}

	var text bytes.Buffer
	err = runDoctorSkills(doctorSkillsOptions{Out: &text})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 {
		t.Fatalf("text err = %v, want exit 2", err)
	}
	if !strings.Contains(text.String(), "Scan issues (1):") {
		t.Fatalf("text report should list scan issues:\n%s", text.String())
	}
}

func TestRunDoctorSkills_EmptyConfig(t *testing.T) {
	setupDoctorSkillsProject(t)
	var out bytes.Buffer
	if err := runDoctorSkills(doctorSkillsOptions{Out: &out, JSON: true}); err != nil {
		t.Fatalf("runDoctorSkills: %v", err)
	}
	var report doctorSkillsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if report.Summary.Configured {
		t.Fatalf("configured should be false with no skill dirs: %+v", report.Summary)
	}
	if report.Summary.Total != 0 {
		t.Fatalf("total = %d, want 0", report.Summary.Total)
	}
}

func TestRunDoctorSkills_TextReport(t *testing.T) {
	projectRoot, _ := setupDoctorSkillsProject(t)
	writeDoctorSkill(t, filepath.Join(projectRoot, ".chord", "skills"), "text-skill", "Text skill", "Body\n")
	var out bytes.Buffer
	if err := runDoctorSkills(doctorSkillsOptions{Out: &out}); err != nil {
		t.Fatalf("runDoctorSkills: %v", err)
	}
	text := out.String()
	for _, want := range []string{"text-skill", "integrity: passed", "visibility: visible", "Summary:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text report missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "says nothing about whether the model will pick it") {
		t.Fatalf("text report should disclaim selection semantics:\n%s", text)
	}
}
