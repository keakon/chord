package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// validateKeymapYAML preserves structural failures without decoding arbitrary
// action values into Go containers. Invalid action types are diagnosed later.
func validateKeymapYAML(root *yaml.Node) error {
	active := make(map[*yaml.Node]bool)
	complete := make(map[*yaml.Node]bool)
	var visit func(*yaml.Node) error
	visit = func(node *yaml.Node) error {
		if node == nil {
			return fmt.Errorf("missing YAML alias target")
		}
		if active[node] {
			return fmt.Errorf("recursive YAML alias at line %d", node.Line)
		}
		if complete[node] {
			return nil
		}
		active[node] = true
		defer delete(active, node)
		if err := keymapDuplicateKeys(node); err != nil {
			return err
		}
		if node.Kind == yaml.AliasNode {
			if err := visit(node.Alias); err != nil {
				return err
			}
		}
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Tag == "!!merge" {
					if err := validateKeymapMerge(node.Content[i+1]); err != nil {
						return err
					}
				}
			}
		}
		for _, child := range node.Content {
			if err := visit(child); err != nil {
				return err
			}
		}
		complete[node] = true
		return nil
	}
	return visit(root)
}

// Match yaml.v3's duplicate comparison and error shape so existing loading
// and project sanitization policies continue to locate duplicate declarations.
func keymapDuplicateKeys(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	var failures []string
	for i := 0; i < len(node.Content); i += 2 {
		first := node.Content[i]
		for j := i + 2; j < len(node.Content); j += 2 {
			next := node.Content[j]
			if first.Kind == next.Kind && first.Value == next.Value {
				failures = append(failures, fmt.Sprintf("line %d: mapping key %#v already defined at line %d", next.Line, next.Value, first.Line))
			}
		}
	}
	if len(failures) > 0 {
		return &yaml.TypeError{Errors: failures}
	}
	return nil
}

func validateKeymapMerge(node *yaml.Node) error {
	target, err := keymapAliasTarget(node)
	if err != nil {
		return err
	}
	if target.Kind == yaml.MappingNode {
		return nil
	}
	if node.Kind == yaml.SequenceNode {
		for _, item := range target.Content {
			mapping, err := keymapAliasTarget(item)
			if err != nil {
				return err
			}
			if mapping.Kind != yaml.MappingNode {
				return fmt.Errorf("YAML merge at line %d must contain mappings", item.Line)
			}
		}
		return nil
	}
	return fmt.Errorf("YAML merge at line %d must contain mappings", node.Line)
}

// decodeConfigOverrideMap applies the keymap field contract before generic
// project-map conversion. Other fields retain their usual YAML decoding.
func decodeConfigOverrideMap(data []byte) (map[string]any, error) {
	var fields map[string]yaml.Node
	if err := yaml.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	result := make(map[string]any, len(fields))
	for name, node := range fields {
		if name == "keymap" {
			bindings, _, err := parseKeymapNode(&node)
			if err != nil {
				return nil, err
			}
			if len(bindings) > 0 {
				values := make(map[string]any, len(bindings))
				for action, keys := range bindings {
					values[action] = keys
				}
				result[name] = values
			}
			continue
		}
		var value any
		if err := node.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode %s: %w", name, err)
		}
		result[name] = value
	}
	return result, nil
}
