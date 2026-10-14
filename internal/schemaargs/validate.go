package schemaargs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
)

// resultSchemaPath prefixes every result-contract violation path, matching the
// path format the tool-argument validator uses (args.files[0].path).
const resultSchemaPath = "result"

// Violation is one read-only violation of a delivered result: the
// structured record (path, offending value, reason) plus the readable
// explanation carried into the rejection message and terminal diagnostics.
type Violation struct {
	Invalid message.InvalidToolArg
	Message string
}

// ValidateResult checks a delegated result against its result
// contract without touching the delivered value. It differs from the
// tool-argument path in both directions that matter here: it keeps the value
// intact (see schemaCheck.readOnly) and it collects every violation so one
// rejection can report them all.
func ValidateResult(result json.RawMessage, schema map[string]any) ([]Violation, error) {
	if len(schema) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(result))
	dec.UseNumber()
	value, err := decodeSchemaJSONValue(dec, resultSchemaPath, nil, 0)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("result must be valid JSON")
		}
		return nil, fmt.Errorf("decode result: %w", err)
	}
	// The token stream accepts a prefix; reject trailing garbage so a document
	// like `{"a":1} extra` is invalid instead of silently dropping the tail.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("result must be valid JSON")
	}
	check := &schemaCheck{readOnly: true, collectAll: true}
	validateSchemaValue(value, schema, resultSchemaPath, check)
	return check.violations, nil
}

// schemaCheck carries the mode and diagnostics of one schema validation pass.
// The tool-argument path rewrites the value it validates (dropping undeclared
// fields and optional explicit nulls) and unwinds at the first violation; the
// delivered-result path must never rewrite what the worker delivered and
// collects every violation instead of only the first.
type schemaCheck struct {
	ignored    *[]message.IgnoredToolArg
	invalid    *[]message.InvalidToolArg
	violations []Violation
	readOnly   bool
	collectAll bool
	err        error
}

// report records one violation. The first violation also becomes the error the
// tool-argument path returns; collectAll additionally keeps every violation,
// with its readable message, for the delivered-result path.
func (c *schemaCheck) report(invalid message.InvalidToolArg, msg string) {
	if c.err == nil {
		c.err = errors.New(msg)
	}
	if c.invalid != nil {
		*c.invalid = append(*c.invalid, invalid)
	}
	if c.collectAll {
		c.violations = append(c.violations, Violation{Invalid: invalid, Message: msg})
	}
}

func (c *schemaCheck) reportValue(path string, value any, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	invalid := message.InvalidToolArg{Path: path, Reason: message.InvalidToolArgReasonInvalid}
	if encoded, err := Encode(value); err == nil {
		invalid.ValueJSON = string(encoded)
	}
	c.report(invalid, msg)
}

func (c *schemaCheck) reportMissing(path string, format string, args ...any) {
	c.report(message.InvalidToolArg{Path: path, Reason: message.InvalidToolArgReasonMissing}, fmt.Sprintf(format, args...))
}

// stop unwinds the traversal as soon as a violation is known, unless the caller
// asked for the complete list.
func (c *schemaCheck) stop() bool { return c.err != nil && !c.collectAll }

// ValidateArguments checks a decoded argument value in place, enforcing
// required fields, types, enums and array coercion against a JSON-schema-like
// description. It returns the first
// violation. Fields the schema does not declare under
// "additionalProperties": false are removed from value rather than rejected,
// and recorded in ignored. An explicit null for an optional declared field is
// removed the same way: it decodes exactly like an omitted field, so it is
// tolerated as an omission instead of failing the call. Validation failures are
// recorded in invalid when a diagnostic sink is provided; nil sinks skip that
// metadata.
func ValidateArguments(value any, schema map[string]any, ignored *[]message.IgnoredToolArg, invalid *[]message.InvalidToolArg) error {
	check := &schemaCheck{ignored: ignored, invalid: invalid}
	validateSchemaValue(value, schema, "args", check)
	return check.err
}

// validateSchemaValue is the recursive core shared by the tool-argument and
// delivered-result paths.
func validateSchemaValue(value any, schema map[string]any, path string, check *schemaCheck) error {
	if len(schema) == 0 {
		return nil
	}
	if enum, ok := schema["enum"]; ok {
		values := schemaToSlice(enum)
		if len(values) > 0 && !valueInEnum(value, values) {
			check.reportValue(path, value, "%s must be one of %s, got %s", path, formatEnum(values), describeJSONValue(value))
			return check.err
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
			check.reportValue(path, value, "%s must be an object, got %s", path, describeJSONValue(value))
			return check.err
		}
		props, _ := schema["properties"].(map[string]any)
		if !check.readOnly {
			for key, raw := range obj {
				if _, ok := props[key]; ok || !disallowAdditionalProperties(schema) {
					continue
				}
				// Record unknown fields before checking required fields so an invalid
				// spelling can be shown alongside the missing canonical field.
				dropIgnoredArgsUnder(check.ignored, path+"."+key)
				if err := appendIgnoredToolArg(check.ignored, path+"."+key, raw, message.IgnoredToolArgReasonUnrecognized); err != nil {
					return err
				}
				delete(obj, key)
			}
		}
		required := requiredFields(schema["required"])
		mandatory := mandatoryFields(schema)
		// An explicit null for an optional declared field is JSON's "no value"
		// spelling and means the same as omitting the field, so tolerate it as
		// an omission instead of failing the call and forcing a model retry.
		// The dropped value is recorded as ignored so the model still learns
		// the parameter took no effect. Fields any required group names stay
		// strict: null there fails the type check below with the same message
		// as any wrong type. The read-only path cannot drop the value, so it
		// skips the same fields during traversal instead.
		if !check.readOnly {
			for key, raw := range obj {
				if raw != nil || mandatory[key] {
					continue
				}
				if _, declared := props[key].(map[string]any); !declared {
					continue
				}
				if err := appendIgnoredToolArg(check.ignored, path+"."+key, raw, message.IgnoredToolArgReasonNull); err != nil {
					return err
				}
				delete(obj, key)
			}
		}
		for _, key := range required {
			if _, ok := obj[key]; !ok {
				check.reportMissing(path+"."+key, "%s.%s is required", path, key)
				if check.stop() {
					return check.err
				}
			}
		}
		for key, raw := range obj {
			childSchema, ok := props[key].(map[string]any)
			if !ok {
				continue
			}
			if check.readOnly && raw == nil && !mandatory[key] {
				continue
			}
			if err := validateSchemaValue(raw, childSchema, path+"."+key, check); err != nil && !check.collectAll {
				return err
			}
		}
		return check.err
	case "array":
		items, ok := value.([]any)
		if !ok {
			// Schemas that opt in via "coerceFromString": true accept a single
			// scalar matching items.type and treat it as a one-element array.
			// This keeps the documented contract array-only while preventing
			// hard failures when models supply a bare string by habit.
			// "coerceFromObject": true does the same for a single object item.
			if !schemaCoercesFromScalar(schema, value) && !schemaCoercesFromObject(schema, value) {
				check.reportValue(path, value, "%s must be an array, got %s", path, describeJSONValue(value))
				return check.err
			}
			items = []any{value}
		}
		if minItems, ok := asInt(schema["minItems"]); ok && len(items) < minItems {
			check.reportValue(path, value, "%s must contain at least %d item(s)", path, minItems)
			if check.stop() {
				return check.err
			}
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for i, item := range items {
			if err := validateSchemaValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, i), check); err != nil && !check.collectAll {
				return err
			}
		}
		return check.err
	case "string":
		if _, ok := value.(string); !ok {
			check.reportValue(path, value, "%s must be a string, got %s", path, describeJSONValue(value))
			return check.err
		}
		return nil
	case "boolean":
		if _, ok := value.(bool); !ok {
			check.reportValue(path, value, "%s must be a boolean, got %s", path, describeJSONValue(value))
			return check.err
		}
		return nil
	case "integer":
		if !isIntegerJSONValue(value) {
			check.reportValue(path, value, "%s must be an integer, got %s", path, describeJSONValue(value))
			return check.err
		}
		return nil
	case "number":
		if !isNumberJSONValue(value) {
			check.reportValue(path, value, "%s must be a number, got %s", path, describeJSONValue(value))
			return check.err
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

// mandatoryFields lists every property some required group of the schema names:
// the object's own "required" plus the "required" of each anyOf alternative.
// A schema that offers several argument spellings (save_artifact's file pair vs
// its result pair, read_artifact's path vs rel_path) can only express that
// through anyOf, which leaves each individual property optional. Treating those
// properties as droppable would silently strip an explicit null and then report
// the field as missing, hiding the real mistake — the null itself — behind a
// message the model cannot act on. anyOf is the only combinator Chord's tool
// schemas use to express a required group; a "not" branch states the opposite
// (a forbidden combination) and is never read here.
func mandatoryFields(schema map[string]any) map[string]bool {
	fields := make(map[string]bool)
	for _, key := range requiredFields(schema["required"]) {
		fields[key] = true
	}
	for _, branch := range schemaBranches(schema["anyOf"]) {
		for _, key := range requiredFields(branch["required"]) {
			fields[key] = true
		}
	}
	return fields
}

// schemaBranches normalizes a combinator's branch list. Schemas written in Go
// use []map[string]any; the same document decoded from JSON arrives as []any.
func schemaBranches(raw any) []map[string]any {
	switch branches := raw.(type) {
	case []map[string]any:
		return branches
	case []any:
		out := make([]map[string]any, 0, len(branches))
		for _, branch := range branches {
			if object, ok := branch.(map[string]any); ok {
				out = append(out, object)
			}
		}
		return out
	default:
		return nil
	}
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

// maxArgValueDisplayRunes caps how much of an offending scalar value is quoted
// in a validation error, so huge string arguments cannot flood tool results.
const maxArgValueDisplayRunes = 80

// describeJSONValue renders the JSON type plus a truncated scalar preview of
// an offending value, e.g. `string "1.0"`, so schema errors show what was
// actually passed instead of only the expected type.
func describeJSONValue(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return "string " + strconv.Quote(truncateDisplayRunes(v, maxArgValueDisplayRunes))
	case json.Number:
		return "number " + truncateDisplayRunes(v.String(), maxArgValueDisplayRunes)
	case bool:
		return "boolean " + strconv.FormatBool(v)
	case []any:
		return fmt.Sprintf("array with %d item(s)", len(v))
	case map[string]any:
		return fmt.Sprintf("object with %d key(s)", len(v))
	default:
		return fmt.Sprintf("%T", value)
	}
}

func truncateDisplayRunes(s string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit]) + "…"
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
