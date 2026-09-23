package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
)

// decodeArgsForSchema decodes raw tool arguments into a generic value ready for
// schema checking, applying any aliases the tool tolerates. Numbers keep their
// literal form so a later re-encode cannot lose precision. A nil ignored sink
// skips recording (and encoding) values shadowed by duplicate object keys.
func decodeArgsForSchema(tool Tool, args json.RawMessage, ignored *[]message.IgnoredToolArg) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	value, err := decodeSchemaJSONValue(dec, "args", ignored, 0)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("arguments must be valid JSON")
		}
		return nil, fmt.Errorf("decode arguments: %w", err)
	}
	// The token stream accepts a prefix; reject trailing garbage so a document
	// like `{"a":1} extra` is invalid instead of silently dropping the tail.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("arguments must be valid JSON")
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

// maxSchemaJSONDepth bounds recursive descent through nested JSON values.
// encoding/json rejects documents nested deeper than its own limit; the token
// stream decoder has no built-in bound, so a maliciously deep document would
// otherwise overflow the stack here.
const maxSchemaJSONDepth = 1000

func decodeSchemaJSONValue(dec *json.Decoder, path string, ignored *[]message.IgnoredToolArg, depth int) (any, error) {
	if depth > maxSchemaJSONDepth {
		return nil, fmt.Errorf("arguments nested deeper than %d levels", maxSchemaJSONDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}

	switch delim {
	case '{':
		obj := make(map[string]any)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("object key must be a string")
			}
			childPath := path + "." + key
			value, err := decodeSchemaJSONValue(dec, childPath, ignored, depth+1)
			if err != nil {
				return nil, err
			}
			if previous, exists := obj[key]; exists {
				if err := appendIgnoredToolArg(ignored, childPath, previous, message.IgnoredToolArgReasonShadowed); err != nil {
					return nil, err
				}
			}
			obj[key] = value
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return obj, nil
	case '[':
		var items []any
		for index := 0; dec.More(); index++ {
			item, err := decodeSchemaJSONValue(dec, fmt.Sprintf("%s[%d]", path, index), ignored, depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func appendIgnoredToolArg(ignored *[]message.IgnoredToolArg, path string, value any, reason message.IgnoredToolArgReason) error {
	// A nil sink means the caller only wants the pass/fail verdict, so skip the
	// encode: ValidateToolArgs runs once per streaming delta and would throw the
	// record away. Encoding cannot fail for a value that came out of the JSON
	// decoder, so no verdict differs between a nil and a non-nil sink.
	if ignored == nil {
		return nil
	}
	encoded, err := encodeSanitizedArgs(value)
	if err != nil {
		return err
	}
	*ignored = append(*ignored, message.IgnoredToolArg{
		Path:      path,
		ValueJSON: string(encoded),
		Reason:    reason,
	})
	return nil
}

// dropIgnoredArgsUnder removes shadowed/ignored records nested under parent
// (directly or through arrays). Once an unrecognized field is dropped whole,
// its children never execute, so earlier duplicate occurrences inside it are
// noise: surfacing both the parent and its children would double-report.
func dropIgnoredArgsUnder(ignored *[]message.IgnoredToolArg, parent string) {
	if ignored == nil {
		return
	}
	out := (*ignored)[:0]
	for _, item := range *ignored {
		if item.Path == parent || strings.HasPrefix(item.Path, parent+".") || strings.HasPrefix(item.Path, parent+"[") {
			continue
		}
		out = append(out, item)
	}
	*ignored = out
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
	sanitized, err := encodeSanitizedArgs(value)
	if err != nil {
		return args, ignored, invalid, err
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
	if err := validateValueAgainstSchema(value, tool.Parameters(), "args", ignoredSink, invalidSink); err != nil {
		return nil, ignored, invalid, fmt.Errorf("arguments do not match %s schema: %w", tool.Name(), err)
	}
	return value, ignored, invalid, nil
}

// encodeSanitizedArgs re-encodes stripped arguments with HTML escaping off.
// The result is replayed to the model as the call's effective arguments, so
// escaping `<`, `>` and `&` would both inflate every JSX, HTML or shell payload
// sixfold per character and hide from the reader what actually ran.
func encodeSanitizedArgs(value any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, fmt.Errorf("re-encode arguments: %w", err)
	}
	// Encode terminates the value with a newline; tool arguments are compared
	// and displayed as bare JSON.
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
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

// resultSchemaPath prefixes every result-contract violation path, matching the
// path format the tool-argument validator uses (args.files[0].path).
const resultSchemaPath = "result"

// ResultSchemaViolation is one read-only violation of a delivered result: the
// structured record (path, offending value, reason) plus the readable
// explanation carried into the rejection message and terminal diagnostics.
type ResultSchemaViolation struct {
	Invalid message.InvalidToolArg
	Message string
}

// ValidateResultAgainstSchema checks a delegated result against its result
// contract without touching the delivered value. It differs from the
// tool-argument path in both directions that matter here: it keeps the value
// intact (see schemaCheck.readOnly) and it collects every violation so one
// rejection can report them all.
func ValidateResultAgainstSchema(result json.RawMessage, schema map[string]any) ([]ResultSchemaViolation, error) {
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
	violations []ResultSchemaViolation
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
		c.violations = append(c.violations, ResultSchemaViolation{Invalid: invalid, Message: msg})
	}
}

func (c *schemaCheck) reportValue(path string, value any, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	invalid := message.InvalidToolArg{Path: path, Reason: message.InvalidToolArgReasonInvalid}
	if encoded, err := encodeSanitizedArgs(value); err == nil {
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

// validateValueAgainstSchema enforces required fields, types, enums and array
// coercion against a JSON-schema-like description and returns the first
// violation. Fields the schema does not declare under
// "additionalProperties": false are removed from value rather than rejected,
// and recorded in ignored. An explicit null for an optional declared field is
// removed the same way: it decodes exactly like an omitted field, so it is
// tolerated as an omission instead of failing the call. Validation failures are
// recorded in invalid when a diagnostic sink is provided; nil sinks skip that
// metadata.
func validateValueAgainstSchema(value any, schema map[string]any, path string, ignored *[]message.IgnoredToolArg, invalid *[]message.InvalidToolArg) error {
	check := &schemaCheck{ignored: ignored, invalid: invalid}
	validateSchemaValue(value, schema, path, check)
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
