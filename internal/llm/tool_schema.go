package llm

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
)

// toolSchema is a tool's JSON Schema as it goes on the wire.
//
// MarshalJSON lists each object's properties in the order of its required
// list, then its anyOf/oneOf branch requirements, then the rest alphabetically.
// This puts source arguments before replacement arguments for tools such as
// edit, while keeping the wire representation deterministic for prompt caching.
// The order is a generation hint, not a guarantee of model argument order.
type toolSchema map[string]any

func (s toolSchema) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	if err := writeSchemaValue(&buf, map[string]any(s)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeSchemaValue(buf *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case map[string]any:
		return writeSchemaObject(buf, v)
	case []map[string]any:
		buf.WriteByte('[')
		for i, elem := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeSchemaObject(buf, elem); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case []any:
		buf.WriteByte('[')
		for i, elem := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeSchemaValue(buf, elem); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(data)
		return nil
	}
}

func writeSchemaObject(buf *bytes.Buffer, obj map[string]any) error {
	if obj == nil {
		buf.WriteString("null")
		return nil
	}
	buf.WriteByte('{')
	for i, key := range slices.Sorted(maps.Keys(obj)) {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeSchemaKey(buf, key); err != nil {
			return err
		}
		props, ok := obj[key].(map[string]any)
		if key != "properties" || !ok || props == nil {
			if err := writeSchemaValue(buf, obj[key]); err != nil {
				return err
			}
			continue
		}
		buf.WriteByte('{')
		for j, name := range schemaPropertyOrder(obj, props) {
			if j > 0 {
				buf.WriteByte(',')
			}
			if err := writeSchemaKey(buf, name); err != nil {
				return err
			}
			if err := writeSchemaValue(buf, props[name]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	}
	buf.WriteByte('}')
	return nil
}

func writeSchemaKey(buf *bytes.Buffer, key string) error {
	data, err := json.Marshal(key)
	if err != nil {
		return err
	}
	buf.Write(data)
	buf.WriteByte(':')
	return nil
}

// schemaPropertyOrder returns the property names of props: required fields in
// declared order, then fields required by an anyOf/oneOf branch, then the rest
// alphabetically.
func schemaPropertyOrder(obj, props map[string]any) []string {
	// seen gives O(1) dedup; order keeps the emission sequence.
	order := make([]string, 0, len(props))
	seen := make(map[string]struct{}, len(props))
	add := func(names []string) {
		for _, name := range names {
			if _, ok := props[name]; !ok {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			order = append(order, name)
		}
	}
	add(RequiredFields(obj))
	for _, combinator := range []string{"anyOf", "oneOf"} {
		switch branches := obj[combinator].(type) {
		case []map[string]any:
			for _, branch := range branches {
				add(RequiredFields(branch))
			}
		case []any:
			for _, branch := range branches {
				if m, ok := branch.(map[string]any); ok {
					add(RequiredFields(m))
				}
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(props)) {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		order = append(order, name)
	}
	return order
}
