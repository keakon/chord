package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

const doctorExplainConfig = "providers:\n  sample:\n    type: responses\n    models:\n      model-1:\n        reasoning: {effort: high}\n        limit: {input: 0, output: 20}\n"

func runDoctorExplainCommand(t *testing.T, args ...string) (doctorConfigReport, string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newDoctorConfigCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	var report doctorConfigReport
	// Cobra normally emits its own error and usage from Execute; RunE's JSON
	// payload is tested directly below, while command flag wiring is separate.
	if jsonErr := json.NewDecoder(strings.NewReader(out.String())).Decode(&report); jsonErr != nil {
		t.Fatalf("invalid report: %v\n%s", jsonErr, out.String())
	}
	return report, out.String(), err
}
func TestDoctorConfigExplainArguments(t *testing.T) {
	setupDoctorConfigHome(t, doctorExplainConfig)
	t.Chdir(t.TempDir())
	for _, tc := range []struct {
		name string
		args []string
		code doctorQueryErrorCode
	}{
		{"missing_field", []string{"--model", "sample/model-1"}, doctorQueryInvalidArguments},
		{"missing_model", []string{"--field", "limit.input"}, doctorQueryInvalidArguments},
		{"empty_model", []string{"--model=", "--field", "limit.input"}, doctorQueryInvalidArguments},
		{"blank_field", []string{"--model", "sample/model-1", "--field", "  "}, doctorQueryInvalidArguments},
		{"bad_field", []string{"--model", "sample/model-1", "--field", "reasoning"}, doctorQueryUnsupportedField},
		{"field_strict", []string{"--model", "sample/model-1", "--field", " limit.input "}, doctorQueryUnsupportedField},
		{"variant", []string{"--model", "sample/model-1@high", "--field", "limit.input"}, doctorQueryUnsupportedVariant},
		{"empty_variant", []string{"--model", "sample/model-1@", "--field", "limit.input"}, doctorQueryUnsupportedVariant},
		{"syntax", []string{"--model", "sample", "--field", "limit.input"}, doctorQueryInvalidArguments},
		{"missing_provider", []string{"--model", "missing/model-1", "--field", "limit.input"}, doctorQueryUnknownModel},
		{"missing_model_name", []string{"--model", "sample/missing", "--field", "limit.input"}, doctorQueryUnknownModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, _, err := runDoctorExplainCommand(t, append(tc.args, "--json")...)
			exit, ok := errors.AsType[cliExitError](err)
			if !ok || exit.code != 2 || report.OK || report.Query == nil || report.Query.Status != doctorQueryError || report.Query.Error.Code != tc.code || report.Query.Result != nil {
				t.Fatalf("report=%+v query=%+v err=%v", report, report.Query, err)
			}
		})
	}
}
func TestDoctorConfigExplainResolvedAndOrdinary(t *testing.T) {
	setupDoctorConfigHome(t, doctorExplainConfig)
	t.Chdir(t.TempDir())
	report, output, err := runDoctorExplainCommand(t, "--json", "--model", " sample / model-1 ", "--field", "limit.input")
	if err != nil || !report.OK || report.Query.Status != doctorQueryResolved || report.Query.ProjectExists || report.Query.Result.Value != float64(0) || report.Query.Result.ValueState != config.ExplainValueSet || report.Query.Result.Source.Kind != config.ExplainSourceGlobal {
		t.Fatalf("%s %v", output, err)
	}
	var plain bytes.Buffer
	err = runDoctorConfig(doctorConfigOptions{Out: &plain, QueryRequested: true, Model: "sample/model-1", Field: "limit.input"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"View: disk", "Value: 0 (set)", "Source status: complete", "selected scalar = 0", "replace", "no_independent_input_limit", "config OK"} {
		if !strings.Contains(plain.String(), want) {
			t.Fatalf("missing %q: %s", want, plain.String())
		}
	}
	ordinary, output, err := runDoctorExplainCommand(t, "--json")
	if err != nil || !ordinary.OK || strings.Contains(output, "\"query\"") {
		t.Fatalf("%s %v", output, err)
	}
}
func TestDoctorConfigExplainKeepsFullValidation(t *testing.T) {
	setupDoctorConfigHome(t, doctorExplainConfig+"unknown_option: true\n")
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir(filepath.Join(dir, ".chord"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".chord", "config.yaml"), []byte("providers:\n  sample:\n    models:\n      model-1:\n        reasoning: {effort: low}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runDoctorConfig(doctorConfigOptions{Out: &out, JSON: true, QueryRequested: true, Model: "sample/model-1", Field: "reasoning.effort"})
	var report doctorConfigReport
	if json.Unmarshal(out.Bytes(), &report) != nil {
		t.Fatalf("not pure JSON: %s", out.String())
	}
	if err == nil || report.OK || report.Query.Status != doctorQueryResolved || report.Query.Result.Value != "low" || !report.Query.ProjectExists || len(report.Files) != 2 {
		t.Fatalf("%s %v", out.String(), err)
	}
}
func TestDoctorConfigExplainLoadFailures(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		missing       bool
	}{
		{"syntax", "[", false},
		{"missing", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupDoctorConfigHome(t, tc.content)
			t.Chdir(t.TempDir())
			if tc.missing {
				p, err := config.ConfigPath()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			err := runDoctorConfig(doctorConfigOptions{Out: &out, JSON: true, QueryRequested: true, Model: "sample/model-1", Field: "limit.input"})
			var report doctorConfigReport
			if json.Unmarshal(out.Bytes(), &report) != nil || err == nil || report.Query.Error.Code != doctorQueryLoadFailed || report.Query.Result != nil || len(report.Errors) == 0 || report.OK {
				t.Fatalf("%s %v", out.String(), err)
			}
		})
	}
}
func TestDoctorConfigExplainRedaction(t *testing.T) {
	const secret = "https://name:password@example.invalid/v1?api_key=secret-token"
	r := config.ModelFieldExplanation{Diagnostics: []config.Diagnostic{{Message: secret}}, Declarations: []config.ExplainDeclaration{{Diagnostics: []config.Diagnostic{{Message: secret}}}}}
	redactModelFieldExplanation(&r)
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "password") || strings.Contains(string(data), "secret-token") {
		t.Fatalf("leaked secret: %s", data)
	}
	var out bytes.Buffer
	report := doctorConfigReport{Files: []doctorConfigFileReport{}, Query: &doctorConfigQuery{View: "disk"}}
	_ = finishDoctorConfigQueryError(&out, true, report, &doctorConfigQueryError{Code: doctorQueryLoadFailed, Message: secret})
	if strings.Contains(out.String(), "password") || strings.Contains(out.String(), "secret-token") {
		t.Fatalf("leaked secret: %s", out.String())
	}
}
