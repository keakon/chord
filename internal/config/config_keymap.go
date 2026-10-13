package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/keybinding"
)

// KeyMapConfig holds valid explicit overrides, including empty lists that disable actions.
type KeyMapConfig map[string][]string

func (m *KeyMapConfig) UnmarshalYAML(node *yaml.Node) error {
	bindings, _, err := parseKeymapNode(node)
	if err != nil {
		return err
	}
	*m = bindings
	return nil
}

type keymapIssue struct {
	Action keybinding.ActionID
	Path   string
	Node   *yaml.Node
	Reason string
}

type keymapLayer struct {
	Bindings KeyMapConfig
	Origins  map[keybinding.ActionID]Origin
	Issues   []keymapIssue
	File     string
}

func keymapAliasTarget(node *yaml.Node) (*yaml.Node, error) {
	seen := make(map[*yaml.Node]bool)
	for node != nil && node.Kind == yaml.AliasNode {
		if seen[node] {
			return nil, fmt.Errorf("recursive YAML alias at line %d", node.Line)
		}
		seen[node] = true
		node = node.Alias
	}
	if node == nil {
		return nil, fmt.Errorf("missing YAML alias target")
	}
	return node, nil
}

func parseKeymapNode(node *yaml.Node) (KeyMapConfig, []keymapIssue, error) {
	result := make(KeyMapConfig)
	// Validate structure without converting rejected values into Go maps.
	if err := validateKeymapYAML(node); err != nil {
		return nil, nil, fmt.Errorf("keymap: %w", err)
	}
	target, err := keymapAliasTarget(node)
	if err != nil {
		return nil, nil, fmt.Errorf("keymap: %w", err)
	}
	if target.Kind != yaml.MappingNode || target.Tag != "!!map" {
		return result, []keymapIssue{{Path: "keymap", Node: node, Reason: "expected a mapping of actions to key lists"}}, nil
	}
	entries, err := YAMLMappingEntries(target)
	if err != nil {
		return nil, nil, fmt.Errorf("keymap: %w", err)
	}
	var issues []keymapIssue
	for _, entry := range entries {
		id := keybinding.ActionID(entry.Key)
		path := "keymap." + entry.Key
		if _, ok := keybinding.Lookup(id); !ok {
			issues = append(issues, keymapIssue{Action: id, Path: path, Node: entry.KeyNode, Reason: "unknown action"})
			continue
		}
		value, err := keymapAliasTarget(entry.Val)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		if value.Kind != yaml.SequenceNode || value.Tag != "!!seq" {
			issues = append(issues, keymapIssue{Action: id, Path: path, Node: entry.Val, Reason: "expected a string list; use [] to disable this shortcut"})
			continue
		}
		keys := make([]string, 0, len(value.Content))
		valid := true
		for i, element := range value.Content {
			actual, err := keymapAliasTarget(element)
			if err != nil {
				return nil, nil, fmt.Errorf("%s[%d]: %w", path, i, err)
			}
			reason := ""
			key := ""
			if actual.Kind != yaml.ScalarNode || actual.Tag != "!!str" {
				reason = "expected a string"
			} else {
				key, err = keybinding.NormalizeKey(actual.Value)
				if err != nil {
					reason = "invalid key " + keymapSafeText(actual.Value)
				}
			}
			if reason != "" {
				issues = append(issues, keymapIssue{Action: id, Path: fmt.Sprintf("%s[%d]", path, i), Node: actual, Reason: reason})
				valid = false
				break
			}
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
		if valid {
			result[entry.Key] = keys
		}
	}
	return result, issues, nil
}

func inspectKeymapLayer(path string, data []byte, layer OriginLayer) (keymapLayer, error) {
	result := keymapLayer{File: path, Origins: make(map[keybinding.ActionID]Origin)}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return result, fmt.Errorf("parse config %s: %w", path, err)
	}
	if len(root.Content) == 0 {
		return result, nil
	}
	if layer == OriginLayerGlobal && keymapDuplicateKeys(root.Content[0]) != nil {
		// The global decoder rejects the entire root mapping on duplicate
		// keys. No declaration in that mapping contributes a binding.
		return result, nil
	}
	if layer == OriginLayerProject {
		// MergeProjectConfig sanitizes duplicate keys before loading. Mirror
		// only that recovery on this inspector-owned tree, preserving original
		// line numbers. The actual loader still reports the decoder errors.
		recoverKeymapDuplicateOrigins(&root)
	}
	entries, err := YAMLMappingEntries(root.Content[0])
	if err != nil {
		return result, fmt.Errorf("parse config %s: %w", path, err)
	}
	for _, entry := range entries {
		if entry.Key != "keymap" {
			continue
		}
		bindings, issues, err := parseKeymapNode(entry.Val)
		if err != nil {
			// Duplicate declarations remain the existing strict loader's responsibility.
			if _, ok := errors.AsType[*yaml.TypeError](err); ok {
				return result, nil
			}
			return result, fmt.Errorf("parse config %s: %w", path, err)
		}
		result.Bindings, result.Issues = bindings, issues
		actions, err := YAMLMappingEntries(entry.Val)
		if err != nil {
			return result, fmt.Errorf("parse config %s keymap: %w", path, err)
		}
		for _, action := range actions {
			if _, ok := bindings[action.Key]; ok {
				result.Origins[keybinding.ActionID(action.Key)] = Origin{Layer: layer, File: path, Line: action.KeyNode.Line, Col: action.KeyNode.Column}
			}
		}
	}
	return result, nil
}

func recoverKeymapDuplicateOrigins(root *yaml.Node) {
	for range maxOverrideSanitizeRounds {
		var probe Config
		err := root.Decode(&probe)
		failures, ok := errors.AsType[*yaml.TypeError](err)
		if !ok {
			return
		}
		var duplicates []string
		for _, failure := range failures.Errors {
			_, message, ok := cutTypeErrorLine(failure)
			if ok && strings.HasPrefix(message, "mapping key ") {
				duplicates = append(duplicates, failure)
			}
		}
		if len(duplicates) == 0 || len(removeFailingNodes(root.Content[0], nil, parseTypeErrorFailures(duplicates))) == 0 {
			return
		}
	}
}
