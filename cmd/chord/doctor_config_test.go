package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
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

// An advisory is listed as a warning and keeps the config OK.
func TestRunDoctorConfigWarningsDoNotFail(t *testing.T) {
	setupDoctorConfigHome(t, "providers:\n  sample:\n    type: chat-completions\n    models:\n      gemini-3-flash:\n")
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	if err := runDoctorConfig(doctorConfigOptions{Out: &out}); err != nil {
		t.Fatalf("runDoctorConfig: %v", err)
	}
	for _, want := range []string{"warning:", "native_thinking: gemini-3", "config OK"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want mention of %q", out.String(), want)
		}
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

// The catalog identity of the effective snapshot is part of the diagnostics
// surface: script consumers get it as JSON, humans as a text line.
func TestRunDoctorConfigReportsCatalog(t *testing.T) {
	setupDoctorConfigHome(t, "providers:\n  sample:\n    type: responses\n")
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	if err := runDoctorConfig(doctorConfigOptions{Out: &out}); err != nil {
		t.Fatalf("runDoctorConfig: %v", err)
	}
	if !strings.Contains(out.String(), "model catalog: "+modelcatalog.Version()) {
		t.Fatalf("output = %q, want the effective catalog version", out.String())
	}

	var jsonOut bytes.Buffer
	if err := runDoctorConfig(doctorConfigOptions{Out: &jsonOut, JSON: true}); err != nil {
		t.Fatalf("runDoctorConfig JSON: %v", err)
	}
	var report doctorConfigReport
	if err := json.Unmarshal(jsonOut.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, jsonOut.String())
	}
	if report.Catalog.Version != modelcatalog.Version() || report.Catalog.FromRefreshCache {
		t.Fatalf("catalog report = %+v, want the embedded snapshot", report.Catalog)
	}
	if report.Catalog.Source == nil || report.Catalog.Source.Repository == "" {
		t.Fatalf("catalog report = %+v, want the upstream source", report.Catalog)
	}
}

func TestDescribeDoctorCatalog(t *testing.T) {
	embedded := describeDoctorCatalog(doctorConfigCatalogReport{
		Version: "2026-10-01.1",
		Source:  &modelcatalog.CatalogSource{Repository: "https://example.invalid/catalog", Revision: "v2026-10-01.1"},
	})
	for _, want := range []string{"2026-10-01.1", "embedded snapshot of https://example.invalid/catalog @ v2026-10-01.1"} {
		if !strings.Contains(embedded, want) {
			t.Fatalf("embedded = %q, want mention of %q", embedded, want)
		}
	}
	cached := describeDoctorCatalog(doctorConfigCatalogReport{
		Version:          "2026-10-02.1",
		FromRefreshCache: true,
		Commit:           "0123456789abcdef",
		CacheDetail:      "cache version 2026-10-01.1 is older than the catalog in effect",
	})
	for _, want := range []string{"refresh cache", "commit 0123456789abcdef", "local cache not in effect: cache version 2026-10-01.1"} {
		if !strings.Contains(cached, want) {
			t.Fatalf("cached = %q, want mention of %q", cached, want)
		}
	}
}

// A model pool reference that does not resolve is a problem of the effective
// config: the files parse cleanly, but doctor still exits 2.
func TestRunDoctorConfigBrokenPoolRef(t *testing.T) {
	setupDoctorConfigHome(t, "providers:\n  sample:\n    type: responses\n    models:\n      test-model:\n        limit:\n          context: 100000\n          output: 64000\nmodel_pools:\n  default:\n    - sample/gone\n")
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	err := runDoctorConfig(doctorConfigOptions{Out: &out})
	if exitErr, ok := errors.AsType[cliExitError](err); !ok || exitErr.code != 2 {
		t.Fatalf("err = %v, want exit 2", err)
	}
	text := out.String()
	if !strings.Contains(text, "problem: model ref \"sample/gone\"") {
		t.Fatalf("output = %q, want the broken pool reference as a problem", text)
	}
	if strings.Contains(text, "config OK") {
		t.Fatalf("output = %q, want no config OK line", text)
	}
}

func TestRunDoctorConfigBrokenPoolRefJSON(t *testing.T) {
	setupDoctorConfigHome(t, "providers:\n  sample:\n    type: responses\n    models:\n      test-model:\n        limit:\n          context: 100000\n          output: 64000\nmodel_pools:\n  default:\n    - sample/gone\n")
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
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "sample/gone") {
		t.Fatalf("report.Errors = %+v, want the broken reference", report)
	}
	for _, f := range report.Files {
		if !f.OK {
			t.Fatalf("file report = %+v, want clean parse issues", f)
		}
	}
}
