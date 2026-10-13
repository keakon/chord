package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/keakon/chord/internal/config"
)

type doctorQueryStatus string

const (
	doctorQueryResolved doctorQueryStatus = "resolved"
	doctorQueryError    doctorQueryStatus = "error"
)

type doctorQueryErrorCode string

const (
	doctorQueryInvalidArguments   doctorQueryErrorCode = "invalid_arguments"
	doctorQueryUnsupportedField   doctorQueryErrorCode = "unsupported_field"
	doctorQueryUnsupportedVariant doctorQueryErrorCode = "unsupported_variant"
	doctorQueryUnknownModel       doctorQueryErrorCode = "unknown_model"
	doctorQueryLoadFailed         doctorQueryErrorCode = "load_failed"
)

type doctorConfigQueryError struct {
	Code    doctorQueryErrorCode `json:"code"`
	Message string               `json:"message"`
}
type doctorConfigQuery struct {
	Status        doctorQueryStatus             `json:"status"`
	View          string                        `json:"view"`
	GlobalPath    string                        `json:"global_path"`
	ProjectPath   string                        `json:"project_path"`
	ProjectExists bool                          `json:"project_exists"`
	Result        *config.ModelFieldExplanation `json:"result,omitempty"`
	Error         *doctorConfigQueryError       `json:"error,omitempty"`
}

func validateDoctorConfigQuery(opts doctorConfigOptions) (string, *doctorConfigQueryError) {
	model, field := strings.TrimSpace(opts.Model), strings.TrimSpace(opts.Field)
	if model == "" || field == "" {
		return "", &doctorConfigQueryError{Code: doctorQueryInvalidArguments, Message: "--model and --field must both be explicitly provided and nonempty"}
	}
	if err := config.ValidateExplainField(config.ExplainField(opts.Field)); err != nil {
		return "", &doctorConfigQueryError{Code: doctorQueryUnsupportedField, Message: err.Error()}
	}
	if strings.Contains(model, "@") {
		return "", &doctorConfigQueryError{Code: doctorQueryUnsupportedVariant, Message: "configuration explanations support base models only; @variants are not supported"}
	}
	base, _ := config.ParseModelRef(model)
	provider, name := config.SplitProviderModelRef(base)
	if provider == "" || name == "" {
		return "", &doctorConfigQueryError{Code: doctorQueryInvalidArguments, Message: "--model must use provider/model syntax"}
	}
	return config.CanonicalModelRef(provider, name, ""), nil
}
func finishDoctorConfigQueryError(out io.Writer, asJSON bool, report doctorConfigReport, queryErr *doctorConfigQueryError) error {
	queryErr.Message = redactConfigDiagnostic(config.Diagnostic{Message: queryErr.Message}).Message
	report.OK = false
	report.Query.Status = doctorQueryError
	report.Query.Error = queryErr
	report.Errors = append(report.Errors, queryErr.Message)
	if err := writeDoctorConfigReport(out, asJSON, report); err != nil {
		return err
	}
	return cliExitError{code: 2, err: fmt.Errorf("%s", queryErr.Message)}
}
func redactModelFieldExplanation(result *config.ModelFieldExplanation) {
	for i, d := range result.Diagnostics {
		result.Diagnostics[i] = redactConfigDiagnostic(d)
	}
	for i := range result.Declarations {
		for j, d := range result.Declarations[i].Diagnostics {
			result.Declarations[i].Diagnostics[j] = redactConfigDiagnostic(d)
		}
	}
}
func renderDoctorConfigQuery(out io.Writer, query *doctorConfigQuery) {
	if query.Error != nil {
		fmt.Fprintf(out, "Configuration query: %s (%s)\n", query.Error.Message, query.Error.Code)
		return
	}
	r := query.Result
	fmt.Fprintf(out, "Model: %s\nField: %s\nView: disk\nGlobal config: %s\nProject config: %s (exists: %t)\n", r.Model, r.Field, query.GlobalPath, query.ProjectPath, query.ProjectExists)
	value, _ := json.Marshal(r.Value)
	fmt.Fprintf(out, "Value: %s (%s)\nSource status: %s\n", value, r.ValueState, r.SourceStatus)
	if r.Source != nil {
		renderExplainSource(out, "Source", r.Source)
	}
	if len(r.Declarations) > 0 {
		fmt.Fprintln(out, "Declarations (lowest priority first):")
		for _, d := range r.Declarations {
			value := ""
			if d.Value != nil {
				encoded, _ := json.Marshal(d.Value)
				value = " = " + string(encoded)
			}
			fmt.Fprintf(out, "  %s %s%s\n", d.Effect, d.Kind, value)
			renderExplainSource(out, "    at", &d.Source)
		}
	}
	for _, d := range r.Diagnostics {
		fmt.Fprintf(out, "Related diagnostic: %s\n", d.String())
	}
	if target := r.EditTarget; target != nil {
		path, _ := json.Marshal(target.PathSegments)
		fmt.Fprintf(out, "Edit: %s %s %s", target.Action, target.File, path)
		if target.Line > 0 {
			fmt.Fprintf(out, " (line %d, column %d)", target.Line, target.Col)
		}
		fmt.Fprintln(out)
	}
	for _, note := range r.Notes {
		fmt.Fprintf(out, "Note [%s]: %s\n", note.Code, note.Message)
	}
	fmt.Fprintln(out)
}
func renderExplainSource(out io.Writer, label string, source *config.ExplainSource) {
	path, _ := json.Marshal(source.PathSegments)
	fmt.Fprintf(out, "%s: %s %s", label, source.Kind, path)
	if source.File != "" {
		fmt.Fprintf(out, " in %s", source.File)
	}
	if source.Line > 0 {
		fmt.Fprintf(out, " (line %d, column %d)", source.Line, source.Col)
	}
	if source.Inherited {
		fmt.Fprint(out, " via shared YAML declaration")
	}
	if source.CatalogModelID != "" {
		fmt.Fprintf(out, " catalog %s @ %s", source.CatalogModelID, source.CatalogVersion)
	}
	if source.Preset != "" {
		fmt.Fprintf(out, " preset %s, wire model %s", source.Preset, source.WireModel)
	}
	if source.Rule != "" {
		fmt.Fprintf(out, " rule %s", source.Rule)
	}
	fmt.Fprintln(out)
	for _, input := range source.Inputs {
		fmt.Fprintf(out, "  %s = %v (%s)\n", input.Field, input.Value, input.SourceStatus)
		if input.Source != nil {
			renderExplainSource(out, "    source", input.Source)
		}
	}
}
