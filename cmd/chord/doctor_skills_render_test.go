package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestRunDoctorSkills_DescriptionSizeFollowsStartupRendering(t *testing.T) {
	for _, tc := range []struct {
		name, frontmatter, permission, reason string
	}{
		{"denied", "", "deny", doctorSkillStartupReasonDenied},
		{"conditional", "paths: [\"src/**\"]\n", "allow", doctorSkillStartupReasonPaths},
		{"invalid paths", "paths: [\"/src/**\"]\n", "allow", doctorSkillStartupReasonPathsInvalid},
		{"manual", "disable-model-invocation: true\n", "allow", doctorSkillStartupReasonManualOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := setupDoctorSkillsProject(t)
			path := writeDoctorSkill(t, filepath.Join(root, ".chord", "skills"), "sample", "Sample description", "Body\n")
			data := "---\nname: sample\ndescription: Sample description\n" + tc.frontmatter + "---\nBody\n"
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			writeDoctorAgentConfig(t, root, "reviewer", "name: reviewer\npermission:\n  skill: "+tc.permission+"\n")
			report, err := runDoctorSkillsJSONWithAgent(t, "reviewer")
			if err != nil {
				t.Fatal(err)
			}
			entry := findDoctorSkillEntry(report, "sample")
			if entry == nil || entry.StartupReason != tc.reason || report.Startup.TotalBytes != 0 {
				t.Fatalf("unexpected startup: entry=%+v, block=%+v", entry, report.Startup)
			}
			assertDoctorDescriptionAbsent(t, *entry)
		})
	}
}

func assertDoctorDescriptionAbsent(t *testing.T, entry doctorSkillEntry) {
	t.Helper()
	desc := entry.Description
	if desc.RenderedBytes == nil || *desc.RenderedBytes != 0 ||
		desc.RenderedChars == nil || *desc.RenderedChars != 0 ||
		desc.Truncated == nil || *desc.Truncated {
		t.Fatalf("unlisted description must be a known zero: %+v", desc)
	}
}

func TestRunDoctorSkills_DescriptionSizeIncludesJSONEscapes(t *testing.T) {
	root, _ := setupDoctorSkillsProject(t)
	desc := "Use \"sample\".\nCheck <value>."
	writeDoctorSkill(t, filepath.Join(root, ".chord", "skills"), "sample", desc, "Body\n")
	report := runDoctorSkillsJSON(t, false)
	entry := findDoctorSkillEntry(report, "sample")
	const rendered = `"Use \"sample\".\nCheck \u003cvalue\u003e."`
	if entry == nil || entry.Description.RenderedBytes == nil || entry.Description.RenderedChars == nil {
		t.Fatalf("missing description size: %+v", entry)
	}
	if *entry.Description.RenderedBytes != len(rendered) || *entry.Description.RenderedChars != len(rendered) {
		t.Fatalf("description = %+v, want %d rendered bytes/chars", entry.Description, len(rendered))
	}
	wantListing := tools.StartupSkillListingHeader + "- name: \"sample\"; description: " + rendered + "\n"
	if report.Startup.TotalBytes != len(wantListing) {
		t.Fatalf("startup bytes=%d, want %d", report.Startup.TotalBytes, len(wantListing))
	}
}

func TestRunDoctorSkills_UnavailableDescriptionSizeIsNull(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprintf("external-%t", external), func(t *testing.T) {
			root, _ := setupDoctorSkillsProject(t)
			writeDoctorSkill(t, filepath.Join(root, ".chord", "skills"), "sample", "Sample description", "Body\n")
			role := "reviewer"
			config := "name: [\n"
			if external {
				config = "name: reviewer\nmode: subagent\nbackend:\n  kind: claude-code\n  command: claude\n"
			}
			writeDoctorAgentConfig(t, root, role, config)
			report, err := runDoctorSkillsJSONWithAgent(t, role)
			if err != nil {
				t.Fatal(err)
			}
			entry := findDoctorSkillEntry(report, "sample")
			if entry == nil || report.Startup.Available {
				t.Fatalf("expected unavailable listing: %+v", report)
			}
			data, err := json.Marshal(entry.Description)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"rendered_bytes", "rendered_chars", "truncated"} {
				if !bytes.Contains(data, []byte(`"`+field+`":null`)) {
					t.Fatalf("unknown %s must be null: %s", field, data)
				}
			}
			var text bytes.Buffer
			renderDoctorSkillsText(&text, report)
			if !strings.Contains(text.String(), "rendered size unavailable") {
				t.Fatalf("missing unavailable size in text: %s", text.String())
			}
		})
	}
}

func TestRunDoctorSkills_RepeatedDirectoryPreservesEachScanOrigin(t *testing.T) {
	root, _ := setupDoctorSkillsProject(t)
	dir := filepath.Join(root, ".chord", "skills")
	writeDoctorSkill(t, dir, "sample", "Sample description", "Body\n")
	cfg := fmt.Sprintf("skills:\n  paths:\n    - %q\n    - %q\n", dir, dir)
	if err := os.WriteFile(filepath.Join(root, ".chord", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	report := runDoctorSkillsJSON(t, false)
	if len(report.Entries) != 3 {
		t.Fatalf("entries=%d, want 3 scan occurrences", len(report.Entries))
	}
	seen := make(map[int]bool)
	for _, entry := range report.Entries {
		if seen[entry.Priority] {
			t.Fatalf("scan priority reused: %+v", entry)
		}
		seen[entry.Priority] = true
		if entry.Shadowed {
			if entry.Source != skillDirOriginExtra || entry.Scope != skillDirScopeExtra ||
				(entry.Priority != 4 && entry.Priority != 5) {
				t.Fatalf("incorrect duplicate origin: %+v", entry)
			}
			assertDoctorDescriptionAbsent(t, entry)
		} else if entry.Source != skillDirOriginProject || entry.Scope != skillDirScopeCheckout || entry.Priority != 1 {
			t.Fatalf("incorrect winning origin: %+v", entry)
		}
	}
}

func TestRunDoctorSkills_StartupTotalCanExceedDescriptionBudget(t *testing.T) {
	root, _ := setupDoctorSkillsProject(t)
	for i := range 100 {
		writeDoctorSkill(t, filepath.Join(root, ".chord", "skills"),
			fmt.Sprintf("%s-%02d", strings.Repeat("a", 200), i), "Sample description", "Body\n")
	}
	report := runDoctorSkillsJSON(t, false)
	if report.Startup.TotalBytes <= report.Startup.DescriptionBudgetBytes || report.Startup.NameOnly == 0 {
		t.Fatalf("fixture must exceed description budget with additional names: %+v", report.Startup)
	}
	for _, entry := range report.Entries {
		if entry.Startup == doctorSkillStartupNameOnly {
			assertDoctorDescriptionAbsent(t, entry)
		}
	}
	data, err := json.Marshal(report.Startup)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"description_budget_bytes":8192`)) || bytes.Contains(data, []byte(`"max_total_bytes"`)) {
		t.Fatalf("budget scope unclear in JSON: %s", data)
	}
	var text bytes.Buffer
	renderDoctorSkillsStartup(&text, report)
	if !strings.Contains(text.String(), fmt.Sprintf("%d bytes / %d chars total", report.Startup.TotalBytes, report.Startup.TotalChars)) ||
		!strings.Contains(text.String(), "additional names excluded") {
		t.Fatalf("budget scope unclear in text: %s", text.String())
	}
}
