package tools

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/schemaargs"
)

// decodeArgsForSchema decodes raw tool arguments into a generic value ready for
// schema checking, applying any aliases the tool tolerates. Numbers keep their
// literal form so a later re-encode cannot lose precision. A nil ignored sink
// skips recording (and encoding) values shadowed by duplicate object keys.
func decodeArgsForSchema(tool Tool, args json.RawMessage, ignored *[]message.IgnoredToolArg) (any, error) {
	value, err := schemaargs.DecodeArguments(args, ignored)
	if err != nil {
		return nil, err
	}

	// Shape tolerated alias values before renaming aliases: the shaper then
	// sees the alias key exactly as written by the model, so canonical fields
	// keep the types declared in Parameters() instead of being reshaped too.
	if shaper, ok := tool.(aliasArgumentValueShaper); ok {
		if obj, ok := value.(map[string]any); ok {
			for key, raw := range obj {
				if shaped, done := shaper.shapeAliasArgument(key, raw); done {
					obj[key] = shaped
				}
			}
		}
	}
	if aliaser, ok := tool.(argumentAliaser); ok {
		value = applyArgumentAliases(value, aliaser.argumentAliases())
	}
	return value, nil
}

// SanitizeUnknownArgsWithDiagnostics validates and sanitizes tool arguments,
// retaining both values that will be ignored and fields that caused schema
// validation to fail. Required fields, types, enums, and array coercion remain
// strict. Fields under "additionalProperties": false are stripped, and
// duplicate object keys use last-value-wins semantics while recording every
// shadowed earlier value. The latter is kept separate because an invalid value
// must be rendered in an error style, while an unrecognized value is merely
// not part of the effective execution arguments.
func SanitizeUnknownArgsWithDiagnostics(tool Tool, args json.RawMessage) (json.RawMessage, []message.IgnoredToolArg, []message.InvalidToolArg, error) {
	if tool == nil {
		return args, nil, nil, nil
	}
	value, ignored, invalid, err := validateToolArgsDecoded(tool, args, true, true)
	sortToolArgDiagnostics(ignored, invalid)
	if err != nil {
		return args, ignored, invalid, err
	}
	// Only re-encode when a value was actually removed. Encoding a map sorts its
	// keys, so returning a re-encoded document for a call that passed cleanly
	// would rewrite arguments the user typed by hand in the permission prompt
	// and show them back reordered. The alias rename that decoding applies is
	// therefore only visible in the effective document of a call that also had
	// something stripped; that is harmless because tools decode both the alias
	// and the canonical field name.
	if len(ignored) == 0 {
		return args, nil, invalid, nil
	}
	sanitized, err := schemaargs.Encode(value)
	if err != nil {
		return args, ignored, invalid, err
	}
	if strict, ok := tool.(StrictArgumentsTool); ok && strict.StrictArguments() && len(ignored) > 0 {
		return args, ignored, invalid, fmt.Errorf("%s rejects unsupported, duplicate or null parameters before execution", tool.Name())
	}
	return sanitized, ignored, invalid, nil
}

func sortToolArgDiagnostics(ignored []message.IgnoredToolArg, invalid []message.InvalidToolArg) {
	sort.SliceStable(ignored, func(i, j int) bool {
		return ignored[i].Path < ignored[j].Path
	})
	sort.SliceStable(invalid, func(i, j int) bool {
		return invalid[i].Path < invalid[j].Path
	})
}

// ValidateToolArgs is the strict half of SanitizeUnknownArgsWithDiagnostics: it
// validates raw JSON against the tool schema and returns the first error, for
// callers that only care whether arguments are rejected. Unrecognized fields
// are stripped rather than rejected, so they never surface as an error here;
// passing a nil ignored sink skips both recording and encoding what was
// stripped, which is the whole cost difference from
// SanitizeUnknownArgsWithDiagnostics.
func ValidateToolArgs(tool Tool, args json.RawMessage) error {
	if tool == nil {
		return nil
	}
	_, _, _, err := validateToolArgsDecoded(tool, args, false, false)
	if err != nil {
		return err
	}
	return nil
}

func validateToolArgsDecoded(tool Tool, args json.RawMessage, recordIgnored, recordInvalid bool) (any, []message.IgnoredToolArg, []message.InvalidToolArg, error) {
	var ignored []message.IgnoredToolArg
	var ignoredSink *[]message.IgnoredToolArg
	if recordIgnored {
		ignoredSink = &ignored
	}
	var invalid []message.InvalidToolArg
	var invalidSink *[]message.InvalidToolArg
	if recordInvalid {
		invalidSink = &invalid
	}
	value, err := decodeArgsForSchema(tool, args, ignoredSink)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := schemaargs.ValidateArguments(value, tool.Parameters(), ignoredSink, invalidSink); err != nil {
		return nil, ignored, invalid, fmt.Errorf("arguments do not match %s schema: %w", tool.Name(), err)
	}
	return value, ignored, invalid, nil
}

// argumentAliaser is implemented by tools that tolerate non-canonical argument
// field names at the model-input boundary. Aliases are honored by validation
// and the tool's own decoding, but are intentionally excluded from Parameters()
// so models only see the canonical field names.
type argumentAliaser interface {
	argumentAliases() map[string]string
}

// aliasArgumentValueShaper is implemented by tools whose tolerated alias value
// carries a different JSON shape than the canonical field, so a plain rename
// would still fail type checking (for example an array where the canonical
// field is a single string). shapeAliasArgument is called before aliases are
// applied, with the alias key exactly as written by the model; it returns the
// shaped value and true when it converted the value, false to leave it for
// ordinary validation (which then reports the mismatch). Implementers must
// only shape values under their own tolerated alias names: canonical fields
// keep the types declared in Parameters() and are validated unchanged.
type aliasArgumentValueShaper interface {
	shapeAliasArgument(aliasKey string, value any) (shaped any, ok bool)
}

// applyArgumentAliases rewrites a decoded argument object in place, renaming
// any tolerated field to its canonical name when the canonical field is not
// already set. The canonical field always wins when both are present.
func applyArgumentAliases(value any, aliases map[string]string) any {
	if len(aliases) == 0 {
		return value
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return value
	}
	for legacy, current := range aliases {
		legacyVal, hasLegacy := obj[legacy]
		if !hasLegacy {
			continue
		}
		if _, hasCurrent := obj[current]; !hasCurrent {
			obj[current] = legacyVal
		}
		delete(obj, legacy)
	}
	return obj
}

// ToleratedArgAliases returns the non-canonical argument spellings the named
// tool accepts, mapped to the canonical field each one stands for. It exposes
// the same maps validation renames through, so code that reads arguments before
// execution (the TUI's streaming preview) recognizes a tolerated spelling
// without re-declaring it. Tools without tolerance return nil.
//
// Maintenance: the switch below lists the built-in tools that tolerate aliases,
// so a new one must be added there. Two tests keep it honest together:
// TestArgumentAliaserImplementationsAreEnumerated parses this package's sources
// for argumentAliases declarations and requires every declaring type to be in
// the test's built-in list, and TestToleratedArgAliasesCoversEveryAliaser
// requires each listed tool to be reachable here with its own map.
func ToleratedArgAliases(toolName string) map[string]string {
	var tool Tool
	switch NormalizeName(toolName) {
	case NameGrep:
		tool = GrepTool{}
	case NameGlob:
		tool = GlobTool{}
	case NameEdit:
		tool = EditTool{}
	default:
		return nil
	}
	aliaser, ok := tool.(argumentAliaser)
	if !ok {
		return nil
	}
	return aliaser.argumentAliases()
}
