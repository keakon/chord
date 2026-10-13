package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/keakon/chord/internal/mcp"
)

func TestMCPListWithoutLLMOrStorageWrites(t *testing.T) {
	t.Chdir(t.TempDir())
	setupDoctorConfigHome(t, "mcp:\n  sample:\n    url: https://example.invalid/mcp?private=query-value\n    oauth:\n      issuer: https://auth.example.invalid\n      client_id: sample-client\n      token_endpoint_auth_method: client_secret_basic\n      client_secret_env: CHORD_SAMPLE_UNSET_SECRET\n")
	t.Setenv("CHORD_SAMPLE_UNSET_SECRET", "")
	var out bytes.Buffer
	cmd := newMCPCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Mode    string         `json:"mode"`
		Targets []mcpListEntry `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("JSON report: %v, %s", err, out.String())
	}
	if report.Mode != "offline" || len(report.Targets) != 1 || report.Targets[0].Auth != mcp.AuthMissing {
		t.Fatalf("unexpected report: %+v", report)
	}
	if strings.Contains(out.String(), "query-value") || strings.Contains(out.String(), "CHORD_SAMPLE_UNSET_SECRET") {
		t.Fatal("private config leaked into list output")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("CHORD_CONFIG_HOME"), "mcp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("list created credential storage: %v", err)
	}
}

func TestMCPDoctorOfflineDoesNotStartStdio(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	marker := filepath.Join(root, "marker")
	setupDoctorConfigHome(t, "mcp:\n  sample:\n    command: sh\n    args: ['-c', 'touch "+marker+"']\n")
	var out bytes.Buffer
	cmd := newDoctorMCPCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var report doctorMCPReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.OK || len(report.Targets) != 1 || len(report.Targets[0].Stages) != 7 {
		t.Fatalf("report = %+v", report)
	}
	for i, stage := range report.Targets[0].Stages {
		if i > 1 && stage.State != "skipped" {
			t.Fatalf("offline network stage = %+v", stage)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("offline doctor started stdio")
	}
}

func TestMCPDoctorReportsMissingHeaderEnvironment(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CHORD_SAMPLE_MCP_HEADER", "")
	setupDoctorConfigHome(t, "mcp:\n  sample:\n    url: https://example.invalid/mcp\n    headers:\n      Authorization: $CHORD_SAMPLE_MCP_HEADER\n")
	var out bytes.Buffer
	cmd := newDoctorMCPCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--json"})
	err := cmd.Execute()
	exitErr, ok := errors.AsType[cliExitError](err)
	if !ok || exitErr.code != 2 {
		t.Fatalf("missing header error = %v", err)
	}
	// Cobra may append an error message when used outside the silent root; decode
	// only the report object, which is written before returning the exit status.
	var report doctorMCPReport
	if err := json.NewDecoder(bytes.NewReader(out.Bytes())).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.OK || report.Targets[0].Stages[0].State != "failed" || report.Targets[0].SuggestedAction != "check_header_environment" {
		t.Fatalf("report = %+v", report)
	}
}

func TestMCPCommandsEmptyAndUnsupportedTargets(t *testing.T) {
	t.Chdir(t.TempDir())
	setupDoctorConfigHome(t, "{}\n")
	var out bytes.Buffer
	for _, cmd := range []*cobra.Command{newMCPListCmd(), newDoctorMCPCmd()} {
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	setupDoctorConfigHome(t, "mcp:\n  sample:\n    command: sample-command\n")
	for _, action := range []string{"login", "logout"} {
		cmd := newMCPCmd()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{action, "sample"})
		err := cmd.Execute()
		exitErr, ok := errors.AsType[cliExitError](err)
		if !ok || exitErr.code != 2 {
			t.Fatalf("%s stdio result = %v", action, err)
		}
	}
}

func TestMCPCommandVisibilityAndAgentOwnership(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	setupDoctorConfigHome(t, "mcp:\n  shared:\n    url: https://example.invalid/mcp\n")
	dir := filepath.Join(root, ".chord", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "explorer.yaml"), []byte("name: explorer\npermission:\n  '*': deny\n  'mcp_private_*': allow\nmcp:\n  private:\n    url: https://private.example.invalid/mcp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	targets, _, err := loadMCPCommandConfig(t.Context(), "explorer", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Ref.Owner != "agent:explorer" || targets[0].Ref.ServerName != "private" {
		t.Fatalf("visible targets = %+v", targets)
	}
	if _, _, err := loadMCPCommandConfig(t.Context(), "explorer", "shared"); err == nil {
		t.Fatal("hidden inherited server resolved")
	}
}

func TestMCPCommandLinkedWorktreeUsesSharedProjectConfiguration(t *testing.T) {
	repo := setupStartupRepo(t)
	setupDoctorConfigHome(t, "{}\n")
	projectDir := filepath.Join(repo, ".chord")
	if err := os.MkdirAll(filepath.Join(projectDir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("mcp:\n  shared:\n    url: https://shared.example.invalid/mcp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "agents", "explorer.yaml"), []byte("name: explorer\npermission: allow\nmcp:\n  private:\n    url: https://private.example.invalid/mcp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	runStartupGit(t, repo, "worktree", "add", "-b", "sample-linked", linked)
	// An ignored checkout-local file must not replace the shared content config.
	if err := os.MkdirAll(filepath.Join(linked, ".chord"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".chord", "config.yaml"), []byte("mcp:\n  other:\n    command: sample-command\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	mainTargets, _, err := loadMCPCommandConfig(t.Context(), "explorer", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(linked)
	linkedTargets, _, err := loadMCPCommandConfig(t.Context(), "explorer", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(mainTargets) != 2 || len(linkedTargets) != 2 {
		t.Fatalf("target counts main=%d linked=%d", len(mainTargets), len(linkedTargets))
	}
	for i := range mainTargets {
		if mainTargets[i].Ref != linkedTargets[i].Ref || mainTargets[i].TargetID != linkedTargets[i].TargetID || mainTargets[i].ProfileID != linkedTargets[i].ProfileID {
			t.Fatalf("linked worktree resolved different configuration or credential identity: main=%+v linked=%+v", mainTargets[i].Ref, linkedTargets[i].Ref)
		}
	}
}
