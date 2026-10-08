package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	outputSchemaURL                = "https://chord.invalid/mcp/output-schema.json"
	outputSchemaWarning            = "[MCP output schema warning]"
	maxOutputSchemaDiagnosticBytes = 240
)

type toolOutputValidator struct {
	schema      *jsonschema.Schema
	unavailable string
}

// Published only after complete discovery, then immutable for concurrent calls.
type toolOutputValidators struct {
	tools map[string]toolOutputValidator
}

// Schema declarations must be self-contained. Never read files or fetch URLs
// supplied by a remote server; document-local references still resolve normally.
type outputSchemaLoader struct{}

func (outputSchemaLoader) Load(string) (any, error) {
	return nil, fmt.Errorf("external output schema resources are not allowed")
}

func compileToolOutputValidators(defs []MCPToolDef) *toolOutputValidators {
	validators := &toolOutputValidators{tools: make(map[string]toolOutputValidator)}
	for _, def := range defs {
		if def.Name == "" || len(def.OutputSchema) == 0 {
			continue
		}
		validators.tools[def.Name] = compileToolOutputValidator(def.OutputSchema)
	}
	return validators
}

func compileToolOutputValidator(raw json.RawMessage) toolOutputValidator {
	// UseNumber preserves exact JSON numbers, including integers above 2^53.
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return toolOutputValidator{unavailable: "outputSchema is not valid JSON"}
	}
	if _, ok := doc.(map[string]any); !ok {
		return toolOutputValidator{unavailable: "outputSchema must be an object"}
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(outputSchemaLoader{})
	if err := compiler.AddResource(outputSchemaURL, doc); err != nil {
		return toolOutputValidator{unavailable: "outputSchema could not be registered"}
	}
	schema, err := compiler.Compile(outputSchemaURL)
	if err != nil {
		// Do not expand a potentially large compilation error tree into tool output.
		return toolOutputValidator{unavailable: "outputSchema is invalid or unsupported; external schema references are disabled"}
	}
	return toolOutputValidator{schema: schema}
}

func (v *toolOutputValidators) warning(toolName string, raw json.RawMessage) string {
	if v == nil {
		return ""
	}
	validator, ok := v.tools[toolName]
	if !ok {
		return ""
	}
	reason := validator.unavailable
	if reason == "" {
		if len(raw) == 0 {
			reason = "structuredContent is missing despite a declared outputSchema"
		} else {
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				reason = "structuredContent could not be decoded for validation"
			} else if err := validator.schema.Validate(doc); err != nil {
				reason = outputSchemaDiagnostic(err)
			}
		}
	}
	if reason == "" {
		return ""
	}
	return outputSchemaWarning + " " + boundedOutputSchemaText(reason) + ". Original result preserved; schema conformance is unverified. The call already ran; do not retry unchanged solely for this warning. Verify any action's effects before repeating it."
}

func outputSchemaDiagnostic(err error) string {
	ve, ok := errors.AsType[*jsonschema.ValidationError](err)
	if !ok {
		return "structuredContent does not conform to outputSchema"
	}
	for len(ve.Causes) > 0 {
		ve = ve.Causes[0]
	}
	// Report one keyword/location, not the full error tree or instance values.
	var pointer strings.Builder
	for _, segment := range ve.InstanceLocation {
		segment = boundedOutputSchemaText(segment)
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
		pointer.WriteByte('/')
		pointer.WriteString(segment)
		if pointer.Len() > maxOutputSchemaDiagnosticBytes {
			break
		}
	}
	return "structuredContent violates " + strconv.Quote(strings.Join(ve.ErrorKind.KeywordPath(), "/")) + " at JSON pointer " + strconv.Quote(boundedOutputSchemaText(pointer.String()))
}

func boundedOutputSchemaText(s string) string {
	if len(s) <= maxOutputSchemaDiagnosticBytes {
		return s
	}
	s = s[:maxOutputSchemaDiagnosticBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "..."
}
