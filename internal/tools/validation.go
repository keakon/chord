package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ValidateToolArgs checks whether raw JSON arguments conform to a tool's
// declared input schema at a basic structural level.
func ValidateToolArgs(tool Tool, args json.RawMessage) error {
	if tool == nil {
		return nil
	}
	if !json.Valid(args) {
		return fmt.Errorf("arguments must be valid JSON")
	}

	var value any
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}

	if aliaser, ok := tool.(legacyArgAliaser); ok {
		value = applyLegacyArgAliases(value, aliaser.legacyArgAliases())
	}

	if err := validateValueAgainstSchema(value, tool.Parameters(), "args"); err != nil {
		return fmt.Errorf("arguments do not match %s schema: %w", tool.Name(), err)
	}
	return nil
}

// legacyArgAliaser is implemented by tools that still accept deprecated
// argument field names mapped onto their current schema fields. Aliases are
// honored by validation and the tool's own decoding for backward compatibility,
// but are intentionally excluded from Parameters() so models only ever see the
// current field names and are not tempted to choose between two spellings.
type legacyArgAliaser interface {
	legacyArgAliases() map[string]string
}

// applyLegacyArgAliases rewrites a decoded argument object in place, renaming
// any present legacy field to its current name when the current field is not
// already set. This lets validation accept legacy field names without exposing
// them in the schema. The current field always wins when both are present.
func applyLegacyArgAliases(value any, aliases map[string]string) any {
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

func validateValueAgainstSchema(value any, schema map[string]any, path string) error {
	if len(schema) == 0 {
		return nil
	}
	if enum, ok := schema["enum"]; ok {
		values := schemaToSlice(enum)
		if len(values) > 0 && !valueInEnum(value, values) {
			return fmt.Errorf("%s must be one of %s", path, formatEnum(values))
		}
	}

	schemaType, _ := schema["type"].(string)
	if schemaType == "" {
		if _, ok := schema["properties"].(map[string]any); ok {
			schemaType = "object"
		}
	}

	switch schemaType {
	case "", "null":
		return nil
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		for _, key := range requiredFields(schema["required"]) {
			if _, ok := obj[key]; !ok {
				return fmt.Errorf("%s.%s is required", path, key)
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for key, raw := range obj {
			childSchema, ok := props[key].(map[string]any)
			if !ok {
				if disallowAdditionalProperties(schema) {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
				continue
			}
			if err := validateValueAgainstSchema(raw, childSchema, path+"."+key); err != nil {
				return err
			}
		}
		return nil
	case "array":
		items, ok := value.([]any)
		if !ok {
			// Schemas that opt in via "coerceFromString": true accept a single
			// scalar matching items.type and treat it as a one-element array.
			// This keeps the documented contract array-only while preventing
			// hard failures when models supply a bare string by habit.
			// "coerceFromObject": true does the same for a single object item.
			if !schemaCoercesFromScalar(schema, value) && !schemaCoercesFromObject(schema, value) {
				return fmt.Errorf("%s must be an array", path)
			}
			items = []any{value}
		}
		if minItems, ok := asInt(schema["minItems"]); ok && len(items) < minItems {
			return fmt.Errorf("%s must contain at least %d item(s)", path, minItems)
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for i, item := range items {
			if err := validateValueAgainstSchema(item, itemSchema, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		return nil
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
		return nil
	case "integer":
		if !isIntegerJSONValue(value) {
			return fmt.Errorf("%s must be an integer", path)
		}
		return nil
	case "number":
		if !isNumberJSONValue(value) {
			return fmt.Errorf("%s must be a number", path)
		}
		return nil
	default:
		return nil
	}
}

func disallowAdditionalProperties(schema map[string]any) bool {
	v, ok := schema["additionalProperties"].(bool)
	return ok && !v
}

// schemaCoercesFromScalar returns true when the schema explicitly opts in to
// accepting a single scalar in place of an array, and the supplied value's
// JSON type matches items.type (or no items.type is declared).
func schemaCoercesFromScalar(schema map[string]any, value any) bool {
	coerce, _ := schema["coerceFromString"].(bool)
	if !coerce {
		return false
	}
	itemSchema, _ := schema["items"].(map[string]any)
	itemType, _ := itemSchema["type"].(string)
	if itemType == "" {
		return true
	}
	switch itemType {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		return isIntegerJSONValue(value)
	case "number":
		return isNumberJSONValue(value)
	default:
		return false
	}
}

// schemaCoercesFromObject returns true when the schema opts in via
// "coerceFromObject": true and the supplied value is a single JSON object for an
// array whose items are objects. This lets a lone item be accepted in place of a
// one-element array (e.g. a single question object), mirroring the scalar
// coercion while keeping the documented contract array-only.
func schemaCoercesFromObject(schema map[string]any, value any) bool {
	coerce, _ := schema["coerceFromObject"].(bool)
	if !coerce {
		return false
	}
	if _, ok := value.(map[string]any); !ok {
		return false
	}
	itemSchema, _ := schema["items"].(map[string]any)
	itemType, _ := itemSchema["type"].(string)
	return itemType == "" || itemType == "object"
}

func requiredFields(raw any) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func schemaToSlice(raw any) []any {
	values, _ := raw.([]any)
	return values
}

func valueInEnum(value any, enum []any) bool {
	for _, candidate := range enum {
		if jsonValueEqual(value, candidate) {
			return true
		}
	}
	return false
}

func jsonValueEqual(a, b any) bool {
	switch av := a.(type) {
	case json.Number:
		return compareNumericJSONValue(av, b)
	case float64:
		return compareNumericJSONValue(av, b)
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	default:
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
	}
}

func compareNumericJSONValue(a any, b any) bool {
	af, aok := toFloat64(a)
	bf, bok := toFloat64(b)
	return aok && bok && af == bf
}

func isIntegerJSONValue(value any) bool {
	switch v := value.(type) {
	case json.Number:
		if _, err := v.Int64(); err == nil {
			return true
		}
		f, err := v.Float64()
		return err == nil && math.Trunc(f) == f
	case float64:
		return math.Trunc(v) == v
	default:
		return false
	}
}

func isNumberJSONValue(value any) bool {
	_, ok := toFloat64(value)
	return ok
}

func toFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func asInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if math.Trunc(v) == v {
			return int(v), true
		}
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func formatEnum(values []any) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%v", value))
	}
	return strings.Join(parts, ", ")
}
