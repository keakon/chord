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

func setupDoctorConfigHome(t *testing.T, content string) {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestRunDoctorConfigOK(t *testing.T) {
	setupDoctorConfigHome(t, "providers:\n  sample:\n    type: responses\n    models:\n      test-model:\n        limit:\n          context: 100000\n          output: 64000\n")
	t.Chdir(t.TempDir()) // no project config

	var out bytes.Buffer
	if err := runDoctorConfig(doctorConfigOptions{Out: &out}); err != nil {
		t.Fatalf("runDoctorConfig: %v", err)
	}
	if !strings.Contains(out.String(), "config OK") {
		t.Fatalf("output = %q, want config OK", out.String())
	}
}

func TestRunDoctorConfigReportsAllIssues(t *testing.T) {
	setupDoctorConfigHome(t, "bogus_top_level: true\nproviders:\n  sample:\n    type: responses\n    retry_backoff: linear\nmax_output_tokens: abc\n")
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	err := runDoctorConfig(doctorConfigOptions{Out: &out})
	exitErr, ok := errors.AsType[cliExitError](err)
	if !ok || exitErr.code != 2 {
		t.Fatalf("err = %v, want exit 2", err)
	}
	for _, want := range []string{"bogus_top_level", "retry_backoff", "cannot unmarshal"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want mention of %q", out.String(), want)
		}
	}
}

func TestRunDoctorConfigChecksProjectConfig(t *testing.T) {
	setupDoctorConfigHome(t, "providers:\n  sample:\n    type: responses\n")
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".chord", "config.yaml"), []byte("provder:\n  x: 1\n"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Chdir(projectRoot)

	var out bytes.Buffer
	err := runDoctorConfig(doctorConfigOptions{Out: &out})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 {
		t.Fatalf("err = %v, want exit 2", err)
	}
	if !strings.Contains(out.String(), "not supported in project config") {
		t.Fatalf("output = %q, want project-field issue", out.String())
	}
}

func TestRunDoctorConfigJSON(t *testing.T) {
	setupDoctorConfigHome(t, "bogus_top_level: true\n")
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	err := runDoctorConfig(doctorConfigOptions{Out: &out, JSON: true})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 {
		t.Fatalf("err = %v, want exit 2", err)
	}
	var report doctorConfigReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out.String())
	}
	if report.OK {
		t.Fatal("report.OK = true, want false")
	}
	if len(report.Files) != 1 || !strings.Contains(strings.Join(report.Files[0].Issues, "\n"), "bogus_top_level") {
		t.Fatalf("report = %+v, want one global file with the unknown-key issue", report)
	}
}

func TestRunDoctorConfigMissingGlobal(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	err := runDoctorConfig(doctorConfigOptions{Out: &out})
	if err == nil || err.Error() != initialSetupRequiredMessage {
		t.Fatalf("err = %v, want initial setup required", err)
	}
}
