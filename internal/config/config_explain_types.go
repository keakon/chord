package config

import "fmt"

// ExplainField is deliberately a closed set of base-model fields.
type ExplainField string

const (
	ExplainReasoningEffort ExplainField = "reasoning.effort"
	ExplainThinkingType    ExplainField = "thinking.type"
	ExplainLimitContext    ExplainField = "limit.context"
	ExplainLimitInput      ExplainField = "limit.input"
	ExplainLimitOutput     ExplainField = "limit.output"
)

type explainFieldDescriptor struct {
	field       ExplainField
	block, leaf string
	get         func(ModelConfig) (ExplainScalar, bool)
}

var explainFields = []explainFieldDescriptor{
	{ExplainReasoningEffort, "reasoning", "effort", func(m ModelConfig) (ExplainScalar, bool) {
		if m.Reasoning == nil {
			return ExplainScalar{Kind: ExplainScalarString}, false
		}
		return ExplainScalar{Kind: ExplainScalarString, StringValue: m.Reasoning.Effort}, true
	}},
	{ExplainThinkingType, "thinking", "type", func(m ModelConfig) (ExplainScalar, bool) {
		if m.Thinking == nil {
			return ExplainScalar{Kind: ExplainScalarString}, false
		}
		return ExplainScalar{Kind: ExplainScalarString, StringValue: m.Thinking.Type}, true
	}},
	{ExplainLimitContext, "limit", "context", func(m ModelConfig) (ExplainScalar, bool) {
		return ExplainScalar{Kind: ExplainScalarInt, IntValue: m.Limit.Context}, true
	}},
	{ExplainLimitInput, "limit", "input", func(m ModelConfig) (ExplainScalar, bool) {
		return ExplainScalar{Kind: ExplainScalarInt, IntValue: m.Limit.Input}, true
	}},
	{ExplainLimitOutput, "limit", "output", func(m ModelConfig) (ExplainScalar, bool) {
		return ExplainScalar{Kind: ExplainScalarInt, IntValue: m.Limit.Output}, true
	}},
}

func explainDescriptor(field ExplainField) (explainFieldDescriptor, error) {
	for _, d := range explainFields {
		if d.field == field {
			return d, nil
		}
	}
	return explainFieldDescriptor{}, fmt.Errorf("unsupported field %q; supported fields: reasoning.effort, thinking.type, limit.context, limit.input, limit.output", field)
}

// ValidateExplainField shares the exact field contract with CLI callers.
func ValidateExplainField(field ExplainField) error { _, err := explainDescriptor(field); return err }

type ExplainScalarKind string

const (
	ExplainScalarString ExplainScalarKind = "string"
	ExplainScalarInt    ExplainScalarKind = "int"
)

type ExplainScalar struct {
	Kind        ExplainScalarKind
	StringValue string
	IntValue    int
}

func (v ExplainScalar) value() any {
	if v.Kind == ExplainScalarString {
		return v.StringValue
	}
	return v.IntValue
}

type ExplainValueState string

const (
	ExplainValueSet     ExplainValueState = "set"
	ExplainValueUnset   ExplainValueState = "unset"
	ExplainValueCleared ExplainValueState = "cleared"
	ExplainValueUnknown ExplainValueState = "unknown"
)

type ExplainSourceStatus string

const (
	ExplainSourceComplete  ExplainSourceStatus = "complete"
	ExplainSourcePartial   ExplainSourceStatus = "partial"
	ExplainSourceUntracked ExplainSourceStatus = "untracked"
)

type ExplainSourceKind string

const (
	ExplainSourceGlobal  ExplainSourceKind = "global"
	ExplainSourceProject ExplainSourceKind = "project"
	ExplainSourceCatalog ExplainSourceKind = "catalog"
	ExplainSourceDerived ExplainSourceKind = "derived"
)

type ExplainDeclarationKind string

const (
	ExplainDeclarationScalar  ExplainDeclarationKind = "scalar"
	ExplainDeclarationNull    ExplainDeclarationKind = "null"
	ExplainDeclarationMapping ExplainDeclarationKind = "mapping"
)

type ExplainEffect string

const (
	ExplainEffectSelected   ExplainEffect = "selected"
	ExplainEffectOverridden ExplainEffect = "overridden"
	ExplainEffectClearing   ExplainEffect = "clearing"
	ExplainEffectIgnored    ExplainEffect = "ignored"
	ExplainEffectObserved   ExplainEffect = "observed"
)

type ExplainEditAction string

const (
	ExplainEditReplace     ExplainEditAction = "replace"
	ExplainEditAddOverride ExplainEditAction = "add_override"
)

type ExplainNoteCode string

const (
	ExplainNoteAncestorNull ExplainNoteCode = "ancestor_null_source_untracked"
	ExplainNoteLeafNull     ExplainNoteCode = "leaf_null_source_untracked"
	ExplainNoteDifferent    ExplainNoteCode = "effective_value_differs"
	ExplainNoteInput        ExplainNoteCode = "no_independent_input_limit"
	ExplainNoteOutput       ExplainNoteCode = "not_request_max_tokens"
	ExplainNoteCatalogBlock ExplainNoteCode = "catalog_requires_absent_block"
	ExplainNoteInherited    ExplainNoteCode = "shared_yaml_declaration"
	ExplainNoteIncomplete   ExplainNoteCode = "source_evidence_incomplete"
	ExplainNoteClear        ExplainNoteCode = "replace_null_block"
)

type ExplainSource struct {
	Kind           ExplainSourceKind `json:"kind"`
	PathSegments   []string          `json:"path_segments"`
	File           string            `json:"file,omitempty"`
	Line           int               `json:"line,omitempty"`
	Col            int               `json:"col,omitempty"`
	Inherited      bool              `json:"inherited,omitempty"`
	CatalogModelID string            `json:"catalog_model_id,omitempty"`
	CatalogVersion string            `json:"catalog_version,omitempty"`
	Preset         string            `json:"preset,omitempty"`
	WireModel      string            `json:"wire_model,omitempty"`
	Rule           string            `json:"rule,omitempty"`
	Inputs         []ExplainInput    `json:"inputs,omitempty"`
}
type ExplainInput struct {
	Field        ExplainField        `json:"field"`
	Value        any                 `json:"value"`
	SourceStatus ExplainSourceStatus `json:"source_status"`
	Source       *ExplainSource      `json:"source,omitempty"`
}
type ExplainDeclaration struct {
	Source      ExplainSource          `json:"source"`
	Kind        ExplainDeclarationKind `json:"kind"`
	Effect      ExplainEffect          `json:"effect"`
	Value       any                    `json:"value,omitempty"`
	Diagnostics []Diagnostic           `json:"diagnostics,omitempty"`
}
type ExplainEditTarget struct {
	File         string            `json:"file"`
	PathSegments []string          `json:"path_segments"`
	Action       ExplainEditAction `json:"action"`
	Line         int               `json:"line,omitempty"`
	Col          int               `json:"col,omitempty"`
}
type ExplainNote struct {
	Code    ExplainNoteCode `json:"code"`
	Message string          `json:"message"`
}
type ModelFieldExplanation struct {
	Model        string               `json:"model"`
	Field        ExplainField         `json:"field"`
	ValueState   ExplainValueState    `json:"value_state"`
	Value        any                  `json:"value"`
	SourceStatus ExplainSourceStatus  `json:"source_status"`
	Source       *ExplainSource       `json:"source,omitempty"`
	Declarations []ExplainDeclaration `json:"declarations"`
	Diagnostics  []Diagnostic         `json:"diagnostics"`
	EditTarget   *ExplainEditTarget   `json:"edit_target,omitempty"`
	Notes        []ExplainNote        `json:"notes"`
}

// ConfigExplainEvidence retains only bounded model-field facts, never YAML or Config.
type ConfigExplainEvidence struct {
	Providers               map[string]map[string]*modelExplainEvidence
	layers                  []explainLayerEvidence
	globalPath, projectPath string
	projectExists           bool
}
type modelExplainEvidence struct {
	Fields  map[ExplainField][]explainRawDeclaration
	Catalog map[ExplainField]explainCatalogAssignment
}
type explainLayerEvidence struct {
	layer      OriginLayer
	file       string
	complete   bool
	limitation string
	ancestors  []explainRawDeclaration
}
type explainRawDeclaration struct {
	source      ExplainSource
	kind        ExplainDeclarationKind
	scalar      *ExplainScalar
	ignored     bool
	editable    bool
	diagnostics []Diagnostic
}
type explainCatalogAssignment struct {
	source ExplainSource
	scalar ExplainScalar
}
type configDroppedPath struct{ Segments []string }
