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

func setupDoctorAgentsProject(t *testing.T) string {
	t.Helper()
	setupDoctorConfigHome(t, "")
	projectRoot := t.TempDir()
	t.Chdir(projectRoot)
	return projectRoot
}

func writeDoctorAgentDef(t *testing.T, projectRoot, name, body string) {
	t.Helper()
	agentsDir := filepath.Join(projectRoot, ".chord", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir agents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write agent definition: %v", err)
	}
}

func runDoctorAgentsJSON(t *testing.T) (doctorAgentsReport, error) {
	t.Helper()
	var out bytes.Buffer
	err := runDoctorAgents(doctorAgentsOptions{Out: &out, JSON: true})
	var report doctorAgentsReport
	if decodeErr := json.Unmarshal(out.Bytes(), &report); decodeErr != nil {
		t.Fatalf("decode JSON report: %v\n%s", decodeErr, out.String())
	}
	return report, err
}

func TestRunDoctorAgentsReportsExternalCommand(t *testing.T) {
	projectRoot := setupDoctorAgentsProject(t)
	command := filepath.Join(projectRoot, "fake-claude")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	writeDoctorAgentDef(t, projectRoot, "external-reviewer", "---\nname: external-reviewer\ndescription: Runs reviews in an external CLI.\nmode: subagent\nbackend:\n  kind: claude-code\n  command: "+command+"\n---\n\nReview the assigned change.\n")

	report, err := runDoctorAgentsJSON(t)
	if err != nil {
		t.Fatalf("runDoctorAgents: %v", err)
	}
	var entry *doctorAgentEntry
	for i := range report.Entries {
		if report.Entries[i].Name == "external-reviewer" {
			entry = &report.Entries[i]
		}
	}
	if entry == nil {
		t.Fatalf("missing external-reviewer entry: %+v", report.Entries)
	}
	if entry.Backend != "claude-code" || entry.CommandStatus != doctorAgentCommandFound {
		t.Fatalf("entry = %+v, want a found claude-code command", entry)
	}
	if report.Summary.CommandMissing != 0 {
		t.Fatalf("summary = %+v, want no missing commands", report.Summary)
	}
}

func TestRunDoctorAgentsLoadFailureExitsTwo(t *testing.T) {
	projectRoot := setupDoctorAgentsProject(t)
	writeDoctorAgentDef(t, projectRoot, "broken-reviewer", "---\nname: broken-reviewer\ndescription: Uses an unknown backend.\nmode: subagent\nbackend:\n  kind: unknown-backend\n---\n\nReview the assigned change.\n")

	report, err := runDoctorAgentsJSON(t)
	exit, ok := errors.AsType[cliExitError](err)
	if !ok || exit.code != 2 {
		t.Fatalf("err = %v, want a cliExitError with code 2", err)
	}
	if report.LoadError == "" || !strings.Contains(report.LoadError, "unknown-backend") {
		t.Fatalf("load error = %q, want the loader's reason", report.LoadError)
	}
	if len(report.Entries) != 0 || report.Summary.Total != 0 {
		t.Fatalf("report = %+v, want no definitions counted after a load failure", report)
	}
}

func TestRunDoctorAgentsFailsOnMissingCommand(t *testing.T) {
	projectRoot := setupDoctorAgentsProject(t)
	writeDoctorAgentDef(t, projectRoot, "external-reviewer", "---\nname: external-reviewer\ndescription: Runs reviews in an external CLI.\nmode: subagent\nbackend:\n  kind: claude-code\n  command: definitely-missing-claude-binary\n---\n\nReview the assigned change.\n")

	report, err := runDoctorAgentsJSON(t)
	if err == nil {
		t.Fatalf("missing command must fail the run; report = %+v", report)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want a missing-command failure", err)
	}
	if report.Summary.CommandMissing != 1 {
		t.Fatalf("summary = %+v, want one missing command", report.Summary)
	}
}
