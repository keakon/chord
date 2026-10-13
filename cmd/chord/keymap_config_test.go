package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeymapCLI(t *testing.T) {
	for _, binding := range []string{`[]`, `["Q"]`} {
		t.Run(binding, func(t *testing.T) {
			withTestStateDir(t)
			globalPath := filepath.Join(flagConfigHome, "config.yaml")
			if err := os.WriteFile(globalPath, []byte("keymap:\n  quit: "+binding+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			projectRoot := t.TempDir()
			projectPath := filepath.Join(projectRoot, ".chord", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(projectPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(projectPath, []byte("# project bindings\n\nkeymap:\n  quit: [\"q\", null]\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			chdirForTest(t, projectRoot)
			plan, err := planInitAppStartup(projectRoot, projectRoot)
			if err != nil {
				t.Fatal(err)
			}
			issues := startupConfigIssues(plan)
			var keymapIssues []string
			for _, issue := range issues {
				if strings.Contains(issue, "keymap.quit") {
					keymapIssues = append(keymapIssues, issue)
				}
			}
			if len(keymapIssues) != 1 {
				t.Fatalf("keymap issues = %q", keymapIssues)
			}
			effective := binding
			if binding == "[]" {
				effective = "disabled"
			}
			for _, want := range []string{projectPath + ":4", "keymap.quit[1]", "expected a string", "entire action ignored", effective, globalPath + ":2"} {
				if !strings.Contains(keymapIssues[0], want) {
					t.Errorf("issue = %q, missing %q", keymapIssues[0], want)
				}
			}
			for _, jsonOutput := range []bool{false, true} {
				var out bytes.Buffer
				err := runDoctorConfig(doctorConfigOptions{Out: &out, JSON: jsonOutput})
				if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 {
					t.Fatalf("doctor error = %v, want exit 2", err)
				}
				rendered := out.String()
				if jsonOutput {
					var report doctorConfigReport
					if err := json.Unmarshal(out.Bytes(), &report); err != nil {
						t.Fatal(err)
					}
					if len(report.Files) != 2 || report.Files[1].OK || len(report.Files[1].Issues) != 1 {
						t.Fatalf("report = %+v", report)
					}
					rendered = report.Files[1].Issues[0]
				}
				if strings.Count(rendered, keymapIssues[0]) != 1 {
					t.Fatalf("doctor output = %q, want startup diagnostic exactly once: %q", rendered, keymapIssues[0])
				}
			}
		})
	}
}

func TestDoctorKeymapDisabledAndDefaultsAreValid(t *testing.T) {
	for _, content := range []string{"{}\n", "keymap:\n  quit: []\n  insert_submit: []\n"} {
		t.Run(content, func(t *testing.T) {
			setupDoctorConfigHome(t, content)
			t.Chdir(t.TempDir())
			var out bytes.Buffer
			if err := runDoctorConfig(doctorConfigOptions{Out: &out}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "config OK") || strings.Contains(out.String(), "keymap.") {
				t.Fatalf("output = %q", out.String())
			}
		})
	}
}

func TestDoctorKeymapShadowingWarnsWithoutRejectingBindings(t *testing.T) {
	setupDoctorConfigHome(t, "keymap:\n  quit: [ctrl+c]\n")
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	if err := runDoctorConfig(doctorConfigOptions{Out: &out, JSON: true}); err != nil {
		t.Fatal(err)
	}
	var report doctorConfigReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.OK || len(report.Files) != 1 || !report.Files[0].OK || len(report.Warnings) != 1 {
		t.Fatalf("report = %+v", report)
	}
	for _, want := range []string{"keymap.quit", "ctrl+c", "shadowed", "bindings retained"} {
		if !strings.Contains(report.Warnings[0], want) {
			t.Errorf("warning = %q, missing %q", report.Warnings[0], want)
		}
	}
}
