package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// YAMLMappingEntry retains declaration nodes so readers can attribute values
// and editors can copy aliases without changing unrelated anchor users.
type YAMLMappingEntry struct {
	Key      string
	KeyNode  *yaml.Node
	Val      *yaml.Node
	ViaMerge bool
}

// YAMLMappingEntries resolves effective mapping declarations. Explicit keys
// win at every nesting level; earlier merge sequence members win over later
// members. Values retain their original nodes, including aliases.
func YAMLMappingEntries(node *yaml.Node) ([]YAMLMappingEntry, error) {
	return yamlMappingEntries(node, make(map[*yaml.Node]bool))
}

func yamlMappingEntries(node *yaml.Node, visiting map[*yaml.Node]bool) ([]YAMLMappingEntry, error) {
	if node == nil {
		return nil, nil
	}
	if visiting[node] {
		return nil, fmt.Errorf("recursive YAML alias or merge at line %d", node.Line)
	}
	visiting[node] = true
	defer delete(visiting, node)
	if node.Kind == yaml.AliasNode {
		return yamlMappingEntries(node.Alias, visiting)
	}
	if node.Kind != yaml.MappingNode {
		return nil, nil
	}
	var entries []YAMLMappingEntry
	positions := make(map[string]int)
	var merges []*yaml.Node
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag == "!!merge" {
			merges = append(merges, value)
			continue
		}
		entry := YAMLMappingEntry{Key: key.Value, KeyNode: key, Val: value}
		if index, exists := positions[key.Value]; exists {
			entries[index] = entry
		} else {
			positions[key.Value] = len(entries)
			entries = append(entries, entry)
		}
	}
	for _, merge := range merges {
		merged, err := yamlMergeEntries(merge, visiting)
		if err != nil {
			return nil, err
		}
		for _, entry := range merged {
			if _, exists := positions[entry.Key]; exists {
				continue
			}
			entry.ViaMerge = true
			positions[entry.Key] = len(entries)
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func yamlMergeEntries(node *yaml.Node, visiting map[*yaml.Node]bool) ([]YAMLMappingEntry, error) {
	if node == nil {
		return nil, nil
	}
	if visiting[node] {
		return nil, fmt.Errorf("recursive YAML alias or merge at line %d", node.Line)
	}
	if node.Kind == yaml.MappingNode {
		return yamlMappingEntries(node, visiting)
	}
	visiting[node] = true
	defer delete(visiting, node)
	switch node.Kind {
	case yaml.AliasNode:
		return yamlMergeEntries(node.Alias, visiting)
	case yaml.SequenceNode:
		var entries []YAMLMappingEntry
		for _, item := range node.Content {
			merged, err := yamlMergeEntries(item, visiting)
			if err != nil {
				return nil, err
			}
			entries = append(entries, merged...)
		}
		return entries, nil
	default:
		return nil, fmt.Errorf("YAML merge at line %d must contain mappings", node.Line)
	}
}
