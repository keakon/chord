package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Accepted size limits for a delegated result contract. The subset below is
// deliberately small, and these bounds keep a hostile or mistaken schema from
// turning every completion check into an unbounded walk. They are constants
// rather than configuration: the accept/reject line is a property of the
// runtime validator, not a tunable.
const (
	maxResultSchemaDepth      = 16
	maxResultSchemaProperties = 256
)

// resultSchemaKeywords is the only vocabulary a delegated result contract may
// use. The runtime validator implements exactly this subset, and silently
// ignoring a keyword the owner wrote (oneOf, patternProperties, ...) would let
// a contract pass while enforcing nothing. additionalProperties is
// intentionally absent: the validator can only honor it by deleting undeclared
// fields, and the delivered-result path must never rewrite the delivered value,
// so a contract is always open and undeclared fields pass.
var resultSchemaKeywords = []string{"type", "required", "properties", "items", "enum", "description"}

// resultSchemaTypes is the only "type" vocabulary a contract may use. A typo
// would otherwise be ignored by the validator, which treats an unknown type as
// "no constraint" and accepts everything.
var resultSchemaTypes = []string{"object", "array", "string", "integer", "number", "boolean"}

// CompileResultSchema validates an incoming result contract against the subset
// the runtime validator implements. It returns the decoded schema used to check
// completions plus its canonical encoding, which is what gets persisted so a
// restored task recompiles exactly the contract it was admitted with. An absent
// schema means the task has no contract and returns nil for both values.
func CompileResultSchema(raw json.RawMessage) (map[string]any, json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil, nil
	}
	if trimmed[0] != '{' {
		return nil, nil, fmt.Errorf("result_schema must be a JSON object")
	}
	var schema map[string]any
	if err := json.Unmarshal(trimmed, &schema); err != nil || schema == nil {
		return nil, nil, fmt.Errorf("result_schema must be a JSON object")
	}
	propertyCount := 0
	if err := validateResultSchemaNode(schema, "result_schema", 1, &propertyCount); err != nil {
		return nil, nil, err
	}
	// A delegated result is always a JSON object, so a contract that does not
	// require an object can never be satisfied.
	if typeName, _ := schema["type"].(string); typeName != "object" {
		return nil, nil, fmt.Errorf("result_schema.type must be \"object\": a delegated result is always a JSON object, so a contract for any other top-level type can never be satisfied")
	}
	canonical, err := encodeSanitizedArgs(schema)
	if err != nil {
		return nil, nil, fmt.Errorf("canonicalize result_schema: %w", err)
	}
	return schema, canonical, nil
}

// validateResultSchemaNode walks one schema node, rejecting anything outside the
// supported subset. Keys and properties are visited in sorted order so a schema
// with several problems always reports the same one.
func validateResultSchemaNode(node map[string]any, path string, depth int, propertyCount *int) error {
	if depth > maxResultSchemaDepth {
		return fmt.Errorf("%s: result_schema nests deeper than %d levels", path, maxResultSchemaDepth)
	}
	for _, key := range slices.Sorted(maps.Keys(node)) {
		if !slices.Contains(resultSchemaKeywords, key) {
			return fmt.Errorf("%s: result_schema keyword %q is not supported; use only %s", path, key, strings.Join(resultSchemaKeywords, ", "))
		}
	}
	if rawType, ok := node["type"]; ok {
		typeName, ok := rawType.(string)
		if !ok {
			return fmt.Errorf("%s.type must be a string", path)
		}
		if !slices.Contains(resultSchemaTypes, typeName) {
			return fmt.Errorf("%s.type %q is not supported; use one of %s", path, typeName, strings.Join(resultSchemaTypes, ", "))
		}
	}
	if rawRequired, ok := node["required"]; ok {
		names, ok := rawRequired.([]any)
		if !ok {
			return fmt.Errorf("%s.required must be an array of property names", path)
		}
		for i, name := range names {
			if _, ok := name.(string); !ok {
				return fmt.Errorf("%s.required[%d] must be a property name (string)", path, i)
			}
		}
	}
	if rawEnum, ok := node["enum"]; ok {
		if _, ok := rawEnum.([]any); !ok {
			return fmt.Errorf("%s.enum must be an array of allowed values", path)
		}
	}
	if rawDescription, ok := node["description"]; ok {
		if _, ok := rawDescription.(string); !ok {
			return fmt.Errorf("%s.description must be a string", path)
		}
	}
	if rawProperties, ok := node["properties"]; ok {
		properties, ok := rawProperties.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.properties must be an object mapping property names to schemas", path)
		}
		for _, name := range slices.Sorted(maps.Keys(properties)) {
			child, ok := properties[name].(map[string]any)
			if !ok {
				return fmt.Errorf("%s.properties.%s must be a schema object", path, name)
			}
			(*propertyCount)++
			if *propertyCount > maxResultSchemaProperties {
				return fmt.Errorf("result_schema declares more than %d properties", maxResultSchemaProperties)
			}
			if err := validateResultSchemaNode(child, path+".properties."+name, depth+1, propertyCount); err != nil {
				return err
			}
		}
	}
	if rawItems, ok := node["items"]; ok {
		items, ok := rawItems.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.items must be a schema object", path)
		}
		if err := validateResultSchemaNode(items, path+".items", depth+1, propertyCount); err != nil {
			return err
		}
	}
	return nil
}
